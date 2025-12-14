package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// handleGetNamespaces lists all namespaces (Simple list view) - UNCHANGED
func handleGetNamespaces(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}

		base := PageBase{
			Title:                "Namespaces",
			ActivePage:           "namespaces",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)

		type nsFetchResult struct {
			ClusterName string
			Namespaces  []string
		}
		
		fetchNS := func(client KubeClient) (nsFetchResult, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			list, err := client.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
			if err != nil {
				return nsFetchResult{}, err
			}
			var names []string
			for _, item := range list.Items {
				names = append(names, item.Name)
			}
			return nsFetchResult{ClusterName: client.ContextName, Namespaces: names}, nil
		}

		results, fetchErrors := ParallelFetch(clients, fetchNS)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		nsMap := make(map[string]*AggregatedNamespaceView)
		clusterStats := make(map[string]int)

		for _, res := range results {
			clusterStats[res.ClusterName] = len(res.Namespaces)
			for _, name := range res.Namespaces {
				if _, ok := nsMap[name]; !ok {
					nsMap[name] = &AggregatedNamespaceView{
						Name:         name,
						StatusCounts: make(map[string]int),
					}
				}
				nsMap[name].Clusters = append(nsMap[name].Clusters, res.ClusterName)
				nsMap[name].StatusCounts["Active"]++
			}
		}

		var allNamespaces []AggregatedNamespaceView
		for _, v := range nsMap {
			sort.Strings(v.Clusters)
			allNamespaces = append(allNamespaces, *v)
		}
		sort.Slice(allNamespaces, func(i, j int) bool { return allNamespaces[i].Name < allNamespaces[j].Name })

		var clusterStatSlice []ClusterStat
		for k, v := range clusterStats {
			clusterStatSlice = append(clusterStatSlice, ClusterStat{Name: k, Count: v})
		}
		sort.Slice(clusterStatSlice, func(i, j int) bool { return clusterStatSlice[i].Name < clusterStatSlice[j].Name })

		data := NamespacePageData{
			PageBase:              base,
			Namespaces:            allNamespaces,
			TotalUniqueNamespaces: len(allNamespaces),
			ClusterStats:          clusterStatSlice,
			TotalNamespaceInstances: len(results),
		}

		return c.Render(200, "all-namespaces.html", data)
	}
}

// handleGetNamespaceDetail fetches comprehensive data for a namespace across clusters
func handleGetNamespaceDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		nsName := c.QueryParam("name")
		if nsName == "" {
			return c.String(400, "Missing 'name' parameter")
		}

		base := PageBase{
			Title:                "NS: " + nsName,
			ActivePage:           "namespaces",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Config error")
		}
		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)

		fetchDetails := func(client KubeClient) (NamespaceDetailView, error) {
			view := NamespaceDetailView{ClusterName: client.ContextName}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			_, err := client.Clientset.CoreV1().Namespaces().Get(ctx, nsName, metav1.GetOptions{})
			if err != nil {
				return view, nil
			}

			var wg sync.WaitGroup
			wg.Add(9)

			go func() {
				defer wg.Done()
				list, _ := client.Clientset.CoreV1().Pods(nsName).List(ctx, metav1.ListOptions{})
				if list != nil {
					view.PodCount = len(list.Items)
					for _, item := range list.Items {
						readyCount := 0
						for _, cs := range item.Status.ContainerStatuses { if cs.Ready { readyCount++ } }
						restartCount := 0
						for _, cs := range item.Status.ContainerStatuses { restartCount += int(cs.RestartCount) }
						view.Pods = append(view.Pods, PodInfo{
							Name: item.Name, Ready: fmt.Sprintf("%d/%d", readyCount, len(item.Spec.Containers)),
							Status: string(item.Status.Phase), Restarts: restartCount, Node: item.Spec.NodeName,
							Age: formatAge(item.CreationTimestamp), PodIP: item.Status.PodIP, Cluster: client.ContextName,
							Reason: getPodReason(item), Namespace: nsName,
						})
					}
				}
			}()

			go func() {
				defer wg.Done()
				list, _ := client.Clientset.AppsV1().Deployments(nsName).List(ctx, metav1.ListOptions{})
				if list != nil {
					view.DeploymentCount = len(list.Items)
					for _, item := range list.Items {
						var images []string
						for _, c := range item.Spec.Template.Spec.Containers { images = append(images, c.Image) }
						desired := int32(1)
						if item.Spec.Replicas != nil { desired = *item.Spec.Replicas }
						view.Deployments = append(view.Deployments, SimpleDeploymentInfo{
							Cluster: client.ContextName, // <--- ADDED
							Name: item.Name, Age: formatAge(item.CreationTimestamp),
							Ready: fmt.Sprintf("%d/%d", item.Status.ReadyReplicas, desired),
							Strategy: string(item.Spec.Strategy.Type), Images: images,
						})
					}
				}
			}()

			go func() {
				defer wg.Done()
				list, _ := client.Clientset.AppsV1().ReplicaSets(nsName).List(ctx, metav1.ListOptions{})
				if list != nil {
					view.ReplicaSetCount = len(list.Items)
					for _, item := range list.Items {
						owner := "None"
						if len(item.OwnerReferences) > 0 { owner = item.OwnerReferences[0].Name }
						desired := int32(1)
						if item.Spec.Replicas != nil { desired = *item.Spec.Replicas }
						view.ReplicaSets = append(view.ReplicaSets, ReplicaSetInfo{
							Cluster: client.ContextName, Namespace: nsName,
							Name: item.Name, Owner: owner, Age: formatAge(item.CreationTimestamp),
							Ready: fmt.Sprintf("%d/%d", item.Status.ReadyReplicas, desired),
						})
					}
				}
			}()

			go func() {
				defer wg.Done()
				list, _ := client.Clientset.AppsV1().DaemonSets(nsName).List(ctx, metav1.ListOptions{})
				if list != nil {
					view.DaemonSetCount = len(list.Items)
					for _, item := range list.Items {
						view.DaemonSets = append(view.DaemonSets, DaemonSetInfo{
							Cluster: client.ContextName, Namespace: nsName,
							Name: item.Name, Age: formatAge(item.CreationTimestamp),
							Ready: fmt.Sprintf("%d/%d", item.Status.NumberReady, item.Status.DesiredNumberScheduled),
						})
					}
				}
			}()

			go func() {
				defer wg.Done()
				list, _ := client.Clientset.AppsV1().StatefulSets(nsName).List(ctx, metav1.ListOptions{})
				if list != nil {
					view.StatefulSetCount = len(list.Items)
					for _, item := range list.Items {
						desired := int32(1)
						if item.Spec.Replicas != nil { desired = *item.Spec.Replicas }
						view.StatefulSets = append(view.StatefulSets, StatefulSetInfo{
							Cluster: client.ContextName, Namespace: nsName,
							Name: item.Name, Age: formatAge(item.CreationTimestamp),
							Ready: fmt.Sprintf("%d/%d", item.Status.ReadyReplicas, desired),
						})
					}
				}
			}()

			go func() {
				defer wg.Done()
				list, _ := client.Clientset.CoreV1().Services(nsName).List(ctx, metav1.ListOptions{})
				if list != nil {
					view.ServiceCount = len(list.Items)
					for _, item := range list.Items {
						ext := ""
						if len(item.Status.LoadBalancer.Ingress) > 0 { ext = item.Status.LoadBalancer.Ingress[0].IP }
						view.Services = append(view.Services, ServiceInfo{
							Name: item.Name, Type: string(item.Spec.Type), ClusterIP: item.Spec.ClusterIP,
							ExternalIP: ext, Age: formatAge(item.CreationTimestamp),
							Cluster: client.ContextName, Namespace: nsName,
						})
					}
				}
			}()

			go func() {
				defer wg.Done()
				list, _ := client.Clientset.NetworkingV1().Ingresses(nsName).List(ctx, metav1.ListOptions{})
				if list != nil {
					view.IngressCount = len(list.Items)
					for _, item := range list.Items {
						hosts := ""
						for _, r := range item.Spec.Rules { hosts += r.Host + " " }
						addr := ""
						if len(item.Status.LoadBalancer.Ingress) > 0 { addr = item.Status.LoadBalancer.Ingress[0].IP }
						view.Ingresses = append(view.Ingresses, IngressInfo{
							Name: item.Name, Hosts: hosts, Address: addr, Age: formatAge(item.CreationTimestamp),
							Cluster: client.ContextName, Namespace: nsName,
						})
					}
				}
			}()

			go func() {
				defer wg.Done()
				list, _ := client.Clientset.CoreV1().ConfigMaps(nsName).List(ctx, metav1.ListOptions{})
				if list != nil {
					view.ConfigMapCount = len(list.Items)
					for _, item := range list.Items {
						view.ConfigMaps = append(view.ConfigMaps, ConfigMapInfo{
							Name: item.Name, DataKeys: len(item.Data), Age: formatAge(item.CreationTimestamp),
							Cluster: client.ContextName, Namespace: nsName,
						})
					}
				}
			}()

			go func() {
				defer wg.Done()
				if CurrentConfig.IsAdmin {
					list, _ := client.Clientset.CoreV1().Secrets(nsName).List(ctx, metav1.ListOptions{})
					if list != nil {
						view.SecretCount = len(list.Items)
						for _, item := range list.Items {
							view.Secrets = append(view.Secrets, SecretInfo{
								Name: item.Name, Type: string(item.Type), KeyCount: len(item.Data),
								Age: formatAge(item.CreationTimestamp), Cluster: client.ContextName, Namespace: nsName,
							})
						}
					}
				}
			}()

			wg.Wait()
			
			evList, _ := client.Clientset.CoreV1().Events(nsName).List(ctx, metav1.ListOptions{Limit: 50})
			if evList != nil {
				for _, e := range evList.Items {
					view.Events = append(view.Events, EventInfo{
						Cluster: client.ContextName, // Add cluster to event
						Type: e.Type, Reason: e.Reason, Message: e.Message, Count: int(e.Count),
						LastSeen: formatAge(e.LastTimestamp), Object: e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name,
					})
				}
			}

			return view, nil
		}

		results, fetchErrors := ParallelFetch(clients, fetchDetails)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		globalStats := NamespaceGlobalStats{
			PodStatus: make(map[string]int),
		}
		
		data := NamespaceDetailPageData{
			PageBase:      base,
			NamespaceName: nsName,
			GlobalStats:   globalStats,
		}

		var clusterNames []string

		// --- AGGREGATION ---
		for _, res := range results {
			if res.PodCount == 0 && res.DeploymentCount == 0 && res.ServiceCount == 0 && res.ConfigMapCount == 0 {
				continue 
			}
			clusterNames = append(clusterNames, res.ClusterName)

			// 1. Stats
			globalStats.TotalPods += res.PodCount
			globalStats.TotalDeployments += res.DeploymentCount
			globalStats.TotalDaemonSets += res.DaemonSetCount
			globalStats.TotalStatefulSets += res.StatefulSetCount
			globalStats.TotalReplicaSets += res.ReplicaSetCount
			globalStats.TotalServices += res.ServiceCount
			globalStats.TotalIngresses += res.IngressCount
			globalStats.TotalConfigMaps += res.ConfigMapCount
			globalStats.TotalSecrets += res.SecretCount

			for _, p := range res.Pods {
				globalStats.PodStatus[p.Status]++
			}
			for _, d := range res.Deployments {
				var r, t int
				fmt.Sscanf(d.Ready, "%d/%d", &r, &t)
				if r == t && t > 0 { globalStats.DeploymentReady++ }
			}
			
			// 2. Append Lists
			data.AllPods = append(data.AllPods, res.Pods...)
			data.AllDeployments = append(data.AllDeployments, res.Deployments...)
			data.AllReplicaSets = append(data.AllReplicaSets, res.ReplicaSets...)
			data.AllDaemonSets = append(data.AllDaemonSets, res.DaemonSets...)
			data.AllStatefulSets = append(data.AllStatefulSets, res.StatefulSets...)
			data.AllServices = append(data.AllServices, res.Services...)
			data.AllIngresses = append(data.AllIngresses, res.Ingresses...)
			data.AllConfigMaps = append(data.AllConfigMaps, res.ConfigMaps...)
			data.AllSecrets = append(data.AllSecrets, res.Secrets...)
			data.AllEvents = append(data.AllEvents, res.Events...)
		}
		
		// Sort the lists for display (Cluster, then Name)
		sort.Slice(data.AllPods, func(i, j int) bool { 
			if data.AllPods[i].Cluster != data.AllPods[j].Cluster { return data.AllPods[i].Cluster < data.AllPods[j].Cluster }
			return data.AllPods[i].Name < data.AllPods[j].Name 
		})
		sort.Slice(data.AllDeployments, func(i, j int) bool { 
			if data.AllDeployments[i].Cluster != data.AllDeployments[j].Cluster { return data.AllDeployments[i].Cluster < data.AllDeployments[j].Cluster }
			return data.AllDeployments[i].Name < data.AllDeployments[j].Name 
		})
		// (Optional: add more sorts for other types if needed)

		sort.Strings(clusterNames)
		data.ClusterNames = clusterNames
		data.GlobalStats = globalStats

		return c.Render(200, "namespace-detail.html", data)
	}
}