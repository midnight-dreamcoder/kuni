package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	// v1 import removed
)

// handleGetDeployments lists and aggregates all deployments from all clusters
func handleGetDeployments(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}
		base := PageBase{
			Title:                "Deployments",
			ActivePage:           "deployments",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
		}
		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		
		deploymentAggregator := make(map[string]*AggregatedDeploymentView)
		clusterDistribution := make(map[string]int)
		
		// Structure to track namespace health for the Treemap
		type nsHealth struct {
			Total     int
			Unhealthy int
		}
		namespaceHealthMap := make(map[string]*nsHealth)

		if len(filesToProcess) == 0 {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("No clusters selected or found matching pattern '%s'", pattern))
		}

		var wg sync.WaitGroup
		var mutex sync.Mutex

		for _, client := range clients {
			wg.Add(1)
			go func(client KubeClient) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				deploymentList, err := client.Clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
				cancel()
				if err != nil {
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list deployments (%v)", client.ContextName, err))
					mutex.Unlock()
					return
				}

				mutex.Lock()
				for _, dep := range deploymentList.Items {
					clusterDistribution[client.ContextName]++
					
					// Namespace Health Logic
					if _, ok := namespaceHealthMap[dep.Namespace]; !ok {
						namespaceHealthMap[dep.Namespace] = &nsHealth{}
					}
					namespaceHealthMap[dep.Namespace].Total++
					
					// Check health
					var desiredReplicas int32 = 1
					if dep.Spec.Replicas != nil {
						desiredReplicas = *dep.Spec.Replicas
					}
					if dep.Status.ReadyReplicas != desiredReplicas {
						namespaceHealthMap[dep.Namespace].Unhealthy++
					}

					// Aggregation Logic
					entry, ok := deploymentAggregator[dep.Name]
					if !ok {
						entry = &AggregatedDeploymentView{
							Name:       dep.Name,
							Clusters:   make([]string, 0),
							Namespaces: make([]string, 0),
							Images:     make([]string, 0),
							Strategies: make([]string, 0),
						}
						deploymentAggregator[dep.Name] = entry
					}
					entry.TotalReadyReplicas += int(dep.Status.ReadyReplicas)
					entry.TotalDesiredReplicas += int(desiredReplicas)
					entry.Clusters = append(entry.Clusters, client.ContextName)
					entry.Namespaces = append(entry.Namespaces, dep.Namespace)
					entry.Strategies = append(entry.Strategies, string(dep.Spec.Strategy.Type))
					for _, container := range dep.Spec.Template.Spec.Containers {
						entry.Images = append(entry.Images, container.Image)
					}
				}
				mutex.Unlock()
			}(client)
		}
		wg.Wait()

		var finalDeployments []AggregatedDeploymentView
		for _, agg := range deploymentAggregator {
			clusterSet := make(map[string]bool); for _, c := range agg.Clusters { clusterSet[c] = true }; agg.Clusters = make([]string, 0, len(clusterSet)); for c := range clusterSet { agg.Clusters = append(agg.Clusters, c) }; sort.Strings(agg.Clusters)
			namespaceSet := make(map[string]bool); for _, ns := range agg.Namespaces { namespaceSet[ns] = true }; agg.Namespaces = make([]string, 0, len(namespaceSet)); for ns := range namespaceSet { agg.Namespaces = append(agg.Namespaces, ns) }; sort.Strings(agg.Namespaces)
			imageSet := make(map[string]bool); for _, img := range agg.Images { imageSet[img] = true }; agg.Images = make([]string, 0, len(imageSet)); for img := range imageSet { agg.Images = append(agg.Images, img) }; sort.Strings(agg.Images)
			strategySet := make(map[string]bool); for _, s := range agg.Strategies { strategySet[s] = true }; agg.Strategies = make([]string, 0, len(strategySet)); for s := range strategySet { agg.Strategies = append(agg.Strategies, s) }; sort.Strings(agg.Strategies)
			finalDeployments = append(finalDeployments, *agg)
		}
		sort.Slice(finalDeployments, func(i, j int) bool { return finalDeployments[i].Name < finalDeployments[j].Name })
		
		var clusterStats []ClusterStat; for n, c := range clusterDistribution { clusterStats = append(clusterStats, ClusterStat{Name: n, Count: c}) }; sort.Slice(clusterStats, func(i, j int) bool { return clusterStats[i].Name < clusterStats[j].Name })
		
		// --- Process Namespace Stats (Base List) ---
		var namespaceStats []NamespaceStat
		for n, h := range namespaceHealthMap {
			color := "#10b981" // Green
			detail := ""
			if h.Unhealthy > 0 {
				color = "#f59e0b" // Orange
				detail = fmt.Sprintf("%d Unhealthy", h.Unhealthy)
			}
			namespaceStats = append(namespaceStats, NamespaceStat{
				Name:        n,
				Count:       h.Total,
				Color:       color,
				ErrorDetail: detail,
			})
		}
		sort.Slice(namespaceStats, func(i, j int) bool {
			return namespaceStats[i].Count > namespaceStats[j].Count
		})

		// --- 1. Prepare Bar Chart Stats (Top 10 + Others) ---
		// We clone the slice logic so we don't affect the Treemap data
		var namespaceBarStats []NamespaceStat
		// Copy all elements first
		namespaceBarStats = append(namespaceBarStats, namespaceStats...)
		
		if len(namespaceBarStats) > 10 {
			var otherSum int
			for _, ns := range namespaceBarStats[10:] {
				otherSum += ns.Count
			}
			// Slice to top 10
			namespaceBarStats = namespaceBarStats[:10]
			// Append Others
			namespaceBarStats = append(namespaceBarStats, NamespaceStat{Name: "Others", Count: otherSum})
		}

		// --- 2. Prepare Treemap Stats (Limit to Top 50) ---
		if len(namespaceStats) > 50 {
			namespaceStats = namespaceStats[:50]
		}

		data := DeploymentPageData{
			PageBase:               base,
			Deployments:            finalDeployments,
			TotalUniqueDeployments: len(deploymentAggregator),
			ClusterStats:           clusterStats,
			NamespaceStats:         namespaceStats,    // For Treemap (Top 50)
			NamespaceBarStats:      namespaceBarStats, // For Bar Chart (Top 10 + Others)
		}
		return c.Render(200, "deployments.html", data)
	}
}

// handleGetDeploymentDetail finds a deployment by NAME across all
// selected clusters and namespaces, and displays a multi-tab view.
func handleGetDeploymentDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		deploymentName := c.QueryParam("name")
		if deploymentName == "" {
			return c.String(400, "Missing required query parameter: name")
		}
		base := PageBase{
			Title:                deploymentName,
			ActivePage:           "deployments",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			IsSearchPage:         false,
			LastRefreshed:        time.Now().Format(time.RFC1123),
		}
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Error finding kubeconfig files: %v", err))
		}
		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		overviews := make(map[string]map[string]DeploymentDetailView)
		podsMap := make(map[string]map[string][]PodInfo)
		nsMap := make(map[string][]string)
		var wg sync.WaitGroup
		var mutex sync.Mutex
		if len(clients) == 0 {
			base.ErrorLogs = append(base.ErrorLogs, "No clusters selected to search.")
		}
		for _, client := range clients {
			wg.Add(1)
			go func(client KubeClient) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				depList, err := client.Clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
				if err != nil {
					log.Printf("ERROR: Failed to list deployments on cluster %s: %v", client.ContextName, err)
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list deployments (%v)", client.ContextName, err))
					mutex.Unlock()
					return
				}
				clusterOverviews := make(map[string]DeploymentDetailView)
				clusterPodsMap := make(map[string][]PodInfo)
				var foundNamespaces []string
				for _, dep := range depList.Items {
					if dep.Name != deploymentName {
						continue
					}
					namespace := dep.Namespace
					var overview DeploymentDetailView
					var pods []PodInfo
					overview.Status = fmt.Sprintf("%d/%d", dep.Status.ReadyReplicas, dep.Status.Replicas)
					overview.Strategy = string(dep.Spec.Strategy.Type)
					if dep.Spec.Strategy.RollingUpdate != nil {
						overview.Strategy = fmt.Sprintf("RollingUpdate (Max Surge: %s, Max Unavail: %s)",
							dep.Spec.Strategy.RollingUpdate.MaxSurge.String(),
							dep.Spec.Strategy.RollingUpdate.MaxUnavailable.String())
					}
					for _, c := range dep.Spec.Template.Spec.Containers {
						overview.Images = append(overview.Images, c.Image)
					}
					for _, cond := range dep.Status.Conditions {
						overview.Conditions = append(overview.Conditions, fmt.Sprintf("%s: %s (%s)", cond.Type, cond.Status, cond.Reason))
					}
					selector, selErr := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
					if selErr == nil {
						overview.Selector = selector.String()
						podList, podErr := client.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
						if podErr == nil {
							for _, pod := range podList.Items {
								readyCount := 0
								for _, cs := range pod.Status.ContainerStatuses {
									if cs.Ready {
										readyCount++
									}
								}
								readyStr := fmt.Sprintf("%d/%d", readyCount, len(pod.Spec.Containers))
								restartCount := 0
								for _, cs := range pod.Status.ContainerStatuses {
									restartCount += int(cs.RestartCount)
								}
								nodeName := pod.Spec.NodeName
								if nodeName == "" {
									nodeName = "N/A"
								}
								pods = append(pods, PodInfo{
									Cluster:   client.ContextName,
									Namespace: pod.Namespace,
									Name:      pod.Name,
									Ready:     readyStr,
									Status:    string(pod.Status.Phase),
									Reason:    getPodReason(pod),
									Restarts:  restartCount,
									Node:      nodeName,
									PodIP:     pod.Status.PodIP,
									QoS:       string(pod.Status.QOSClass),
									Age:       formatAge(pod.CreationTimestamp),
								})
							}
						}
					}
					clusterOverviews[namespace] = overview
					clusterPodsMap[namespace] = pods
					foundNamespaces = append(foundNamespaces, namespace)
				}
				if len(foundNamespaces) > 0 {
					sort.Strings(foundNamespaces)
					mutex.Lock()
					overviews[client.ContextName] = clusterOverviews
					podsMap[client.ContextName] = clusterPodsMap
					nsMap[client.ContextName] = foundNamespaces
					mutex.Unlock()
				}
			}(client)
		}
		wg.Wait()
		var clusterNames []string
		for cn := range overviews {
			clusterNames = append(clusterNames, cn)
		}
		sort.Strings(clusterNames)
		data := DeploymentDetailPageData{
			PageBase:       base,
			DeploymentName: deploymentName,
			Overviews:      overviews,
			Pods:           podsMap,
			ClusterNames:   clusterNames,
			NamespaceNames: nsMap,
		}
		return c.Render(200, "deployment-detail.html", data)
	}
}

// handleGetReplicaSets lists all replicasets
func handleGetReplicaSets(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}
		base := PageBase{
			Title:                "All ReplicaSets",
			ActivePage:           "replicasets",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			IsSearchPage:         false,
			LastRefreshed:        time.Now().Format(time.RFC1123),
		}
		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		var allReplicaSets []ReplicaSetInfo
		clusterDistribution := make(map[string]int)
		namespaceDistribution := make(map[string]int)
		ownerDistribution := make(map[string]int)

		if len(filesToProcess) == 0 {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("No clusters selected or found matching pattern '%s'", pattern))
		}

		var wg sync.WaitGroup
		var mutex sync.Mutex

		for _, client := range clients {
			wg.Add(1)
			go func(client KubeClient) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				rsList, err := client.Clientset.AppsV1().ReplicaSets("").List(ctx, metav1.ListOptions{})
				cancel()
				if err != nil {
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list replicasets (%v)", client.ContextName, err))
					mutex.Unlock()
					return
				}

				mutex.Lock()
				for _, rs := range rsList.Items {
					clusterDistribution[client.ContextName]++
					namespaceDistribution[rs.Namespace]++
					var desiredReplicas int32
					if rs.Spec.Replicas != nil {
						desiredReplicas = *rs.Spec.Replicas
					}
					readyStr := fmt.Sprintf("%d/%d/%d", rs.Status.ReadyReplicas, desiredReplicas, rs.Status.Replicas)
					owner := "None"
					if len(rs.OwnerReferences) > 0 {
						owner = rs.OwnerReferences[0].Name
					}
					ownerDistribution[owner]++
					allReplicaSets = append(allReplicaSets, ReplicaSetInfo{
						Cluster:   client.ContextName,
						Namespace: rs.Namespace,
						Name:      rs.Name,
						Ready:     readyStr,
						Owner:     owner,
						Age:       formatAge(rs.CreationTimestamp),
					})
				}
				mutex.Unlock()
			}(client)
		}
		wg.Wait()

		var clusterStats []ClusterStat; for n, c := range clusterDistribution { clusterStats = append(clusterStats, ClusterStat{Name: n, Count: c}) }; sort.Slice(clusterStats, func(i, j int) bool { return clusterStats[i].Name < clusterStats[j].Name })
		
		const topN = 10
		var namespaceStats []NamespaceStat
		for n, c := range namespaceDistribution {
			namespaceStats = append(namespaceStats, NamespaceStat{Name: n, Count: c})
		}
		sort.Slice(namespaceStats, func(i, j int) bool {
			return namespaceStats[i].Count > namespaceStats[j].Count
		})
		if len(namespaceStats) > topN {
			var otherSum int
			for _, ns := range namespaceStats[topN:] {
				otherSum += ns.Count
			}
			namespaceStats = namespaceStats[:topN]
			namespaceStats = append(namespaceStats, NamespaceStat{Name: "Others", Count: otherSum})
		}

		var ownerStats []NamespaceStat
		for n, c := range ownerDistribution {
			ownerStats = append(ownerStats, NamespaceStat{Name: n, Count: c})
		}
		sort.Slice(ownerStats, func(i, j int) bool {
			return ownerStats[i].Count > ownerStats[j].Count
		})
		if len(ownerStats) > topN {
			var otherSum int
			for _, o := range ownerStats[topN:] {
				otherSum += o.Count
			}
			ownerStats = ownerStats[:topN]
			ownerStats = append(ownerStats, NamespaceStat{Name: "Others", Count: otherSum})
		}

		data := ReplicaSetPageData{
			PageBase:         base,
			ReplicaSets:      allReplicaSets,
			TotalReplicaSets: len(allReplicaSets),
			ClusterStats:     clusterStats,
			NamespaceStats:   namespaceStats,
			OwnerStats:       ownerStats,
		}
		return c.Render(200, "replicasets.html", data)
	}
}

// handleGetReplicaSetDetail fetches a single replicaset
func handleGetReplicaSetDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		clusterContextName := c.QueryParam("cluster_name")
		namespace := c.QueryParam("namespace")
		rsName := c.QueryParam("name")
		if clusterContextName == "" || namespace == "" || rsName == "" {
			return c.String(400, "Missing required query parameters: cluster_name, namespace, name")
		}
		base := PageBase{
			Title:                rsName,
			ActivePage:           "replicasets",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
		}
		clientset, err := findClient(pattern, clusterContextName)
		if err != nil {
			base.ErrorLogs = append(base.ErrorLogs, err.Error())
			return c.Render(200, "replicaset-detail.html", ReplicaSetDetailPageData{PageBase: base})
		}
		data := ReplicaSetDetailPageData{
			PageBase:       base,
			ClusterName:    clusterContextName,
			NamespaceName:  namespace,
			ReplicaSetName: rsName,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rs, err := clientset.AppsV1().ReplicaSets(namespace).Get(ctx, rsName, metav1.GetOptions{})
		if err != nil {
			data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get replicaset: %v", err))
		} else {
			var desiredReplicas int32 = 1
			if rs.Spec.Replicas != nil {
				desiredReplicas = *rs.Spec.Replicas
			}
			data.Status = fmt.Sprintf("%d/%d", rs.Status.ReadyReplicas, desiredReplicas)
			data.Age = formatAge(rs.CreationTimestamp)
			if len(rs.OwnerReferences) > 0 {
				data.OwnerName = rs.OwnerReferences[0].Name
				data.OwnerKind = rs.OwnerReferences[0].Kind
			}
			selector, selErr := metav1.LabelSelectorAsSelector(rs.Spec.Selector)
			if selErr != nil {
				data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to build label selector: %v", selErr))
			} else {
				data.Selector = selector.String()
				podList, podErr := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
				if podErr != nil {
					data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to list pods: %v", podErr))
				} else {
					for _, pod := range podList.Items {
						readyCount := 0; for _, cs := range pod.Status.ContainerStatuses { if cs.Ready { readyCount++ } }
						readyStr := fmt.Sprintf("%d/%d", readyCount, len(pod.Spec.Containers))
						restartCount := 0; for _, cs := range pod.Status.ContainerStatuses { restartCount += int(cs.RestartCount) }
						nodeName := pod.Spec.NodeName; if nodeName == "" { nodeName = "N/A" }
						data.Pods = append(data.Pods, PodInfo{
							Cluster:   clusterContextName,
							Namespace: pod.Namespace,
							Name:      pod.Name,
							Ready:     readyStr,
							Status:    string(pod.Status.Phase),
							Reason:    getPodReason(pod),
							Restarts:  restartCount,
							Node:      nodeName,
							PodIP:     pod.Status.PodIP,
							QoS:       string(pod.Status.QOSClass),
							Age:       formatAge(pod.CreationTimestamp),
						})
					}
				}
			}
		}
		fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", rsName, namespace)
		eventList, err := clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: fieldSelector})
		if err != nil {
			data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get events: %v", err))
		} else {
			sort.Slice(eventList.Items, func(i, j int) bool {
				return eventList.Items[i].LastTimestamp.Time.After(eventList.Items[j].LastTimestamp.Time)
			})
			for _, e := range eventList.Items {
				data.Events = append(data.Events, EventInfo{
					Type: e.Type, Reason: e.Reason, Message: e.Message,
					Count: int(e.Count), LastSeen: formatAge(e.LastTimestamp),
				})
			}
		}
		return c.Render(200, "replicaset-detail.html", data)
	}
}