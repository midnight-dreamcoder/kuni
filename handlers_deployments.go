package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func handleGetDeployments(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		// UPDATED: Use GetBaseData for consistent Auth/View context
		base := GetBaseData(c, "Deployments", "deployments")

		// FIXED: configsToProcess
		configsToProcess, err := getConfigsToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding configs")
		}

		// FIXED: pass configsToProcess
		clients, clientErrors := createClients(configsToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)

		type depResult struct {
			ClusterName string
			Items       []AggregatedDeploymentView
			ClusterStat ClusterStat
			NSStats     map[string]int
		}

		fetchDeployments := func(client KubeClient) (depResult, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			list, err := client.Clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
			if err != nil {
				return depResult{}, err
			}

			var items []AggregatedDeploymentView
			nsCount := make(map[string]int)

			for _, dep := range list.Items {
				var images []string
				for _, container := range dep.Spec.Template.Spec.Containers {
					images = append(images, container.Image)
				}
				var replicas int32 = 1
				if dep.Spec.Replicas != nil {
					replicas = *dep.Spec.Replicas
				}
				items = append(items, AggregatedDeploymentView{
					Name:                 dep.Name,
					TotalReadyReplicas:   int(dep.Status.ReadyReplicas),
					TotalDesiredReplicas: int(replicas),
					Namespaces:           []string{dep.Namespace},
					Clusters:             []string{client.ContextName},
					Images:               images,
					Strategies:           []string{string(dep.Spec.Strategy.Type)},
				})
				nsCount[dep.Namespace]++
			}
			return depResult{
				ClusterName: client.ContextName,
				Items:       items,
				ClusterStat: ClusterStat{Name: client.ContextName, Count: len(items)},
				NSStats:     nsCount,
			}, nil
		}

		results, fetchErrors := ParallelFetch(clients, fetchDeployments)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		// Aggregation Logic (Unchanged)
		globalDepMap := make(map[string]*AggregatedDeploymentView)
		var clusterStats []ClusterStat
		globalNSStats := make(map[string]int)

		for _, res := range results {
			clusterStats = append(clusterStats, res.ClusterStat)
			for ns, count := range res.NSStats {
				globalNSStats[ns] += count
			}
			for _, item := range res.Items {
				if existing, ok := globalDepMap[item.Name]; ok {
					existing.TotalReadyReplicas += item.TotalReadyReplicas
					existing.TotalDesiredReplicas += item.TotalDesiredReplicas
					if !contains(existing.Clusters, item.Clusters[0]) {
						existing.Clusters = append(existing.Clusters, item.Clusters[0])
					}
					if !contains(existing.Namespaces, item.Namespaces[0]) {
						existing.Namespaces = append(existing.Namespaces, item.Namespaces[0])
					}
					// Merge images/strategies simply
					for _, img := range item.Images {
						if !contains(existing.Images, img) {
							existing.Images = append(existing.Images, img)
						}
					}
				} else {
					// Make a copy to avoid pointer issues with loop var
					newItem := item
					globalDepMap[item.Name] = &newItem
				}
			}
		}

		var finalDeps []AggregatedDeploymentView
		for _, dep := range globalDepMap {
			finalDeps = append(finalDeps, *dep)
		}
		sort.Slice(finalDeps, func(i, j int) bool { return finalDeps[i].Name < finalDeps[j].Name })
		sort.Slice(clusterStats, func(i, j int) bool { return clusterStats[i].Name < clusterStats[j].Name })

		var nsStats []NamespaceStat
		for k, v := range globalNSStats {
			nsStats = append(nsStats, NamespaceStat{Name: k, Count: v})
		}
		sort.Slice(nsStats, func(i, j int) bool { return nsStats[i].Count > nsStats[j].Count })

		data := DeploymentPageData{
			PageBase:               base,
			Deployments:            finalDeps,
			TotalUniqueDeployments: len(finalDeps),
			ClusterStats:           clusterStats,
			NamespaceBarStats:      nsStats,
		}

		return c.Render(200, "deployments.html", data)
	}
}

// handleGetDeploymentDetail (Updated to use GetBaseData)
func handleGetDeploymentDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		name := c.QueryParam("name")

		// UPDATED: Use GetBaseData
		base := GetBaseData(c, name, "deployments")

		// FIXED: configsToProcess
		configsToProcess, err := getConfigsToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding configs")
		}

		clients, _ := createClients(configsToProcess)

		data := DeploymentDetailPageData{
			PageBase:       base,
			DeploymentName: name,
			Overviews:      make(map[string]map[string]DeploymentDetailView),
			Pods:           make(map[string]map[string][]PodInfo),
			NamespaceNames: make(map[string][]string),
		}

		var clusterNames []string
		var mutex sync.Mutex

		fetchDetail := func(client KubeClient) (bool, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// List all deployments in all namespaces for this client
			// Optimization: We could use FieldSelector if name is exact match
			list, err := client.Clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, err
			}

			found := false
			for _, d := range list.Items {
				if d.Name == name {
					found = true
					mutex.Lock()
					if _, ok := data.Overviews[client.ContextName]; !ok {
						data.Overviews[client.ContextName] = make(map[string]DeploymentDetailView)
						data.Pods[client.ContextName] = make(map[string][]PodInfo)
						data.NamespaceNames[client.ContextName] = []string{}
					}

					var images []string
					for _, c := range d.Spec.Template.Spec.Containers {
						images = append(images, c.Image)
					}

					// Fetch Pods for this deployment (Label Selector)
					selectorStr := metav1.FormatLabelSelector(d.Spec.Selector)
					podList, _ := client.Clientset.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selectorStr})

					var pods []PodInfo
					for _, p := range podList.Items {
						reason := getPodReason(p)
						pods = append(pods, PodInfo{
							Name: p.Name, Status: string(p.Status.Phase),
							Ready: fmt.Sprintf("%d/%d", countReadyContainers(p.Status.ContainerStatuses), len(p.Spec.Containers)),
							PodIP: p.Status.PodIP, Node: p.Spec.NodeName, Age: formatAge(p.CreationTimestamp), Reason: reason,
						})
					}

					data.Overviews[client.ContextName][d.Namespace] = DeploymentDetailView{
						Status:   fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, *d.Spec.Replicas),
						Strategy: string(d.Spec.Strategy.Type),
						Selector: selectorStr,
						Images:   images,
					}
					data.Pods[client.ContextName][d.Namespace] = pods
					data.NamespaceNames[client.ContextName] = append(data.NamespaceNames[client.ContextName], d.Namespace)
					mutex.Unlock()
				}
			}
			return found, nil
		}

		ParallelFetch(clients, fetchDetail)

		for cName := range data.Overviews {
			clusterNames = append(clusterNames, cName)
		}
		sort.Strings(clusterNames)
		data.ClusterNames = clusterNames

		return c.Render(200, "deployment-detail.html", data)
	}
}

func contains(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}

func countReadyContainers(statuses []v1.ContainerStatus) int {
	c := 0
	for _, s := range statuses {
		if s.Ready {
			c++
		}
	}
	return c
}