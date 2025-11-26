package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	// v1 import removed
)

// handleGetDaemonSets lists all daemonsets
func handleGetDaemonSets(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}
		base := PageBase{
			Title:                "All DaemonSets",
			ActivePage:           "daemonsets",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
		}
		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		var allDaemonSets []DaemonSetInfo
		clusterDistribution := make(map[string]int)
		namespaceDistribution := make(map[string]int)
		nodeSelectorDistribution := make(map[string]int)

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
				dsList, err := client.Clientset.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{})
				if err != nil {
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list daemonsets (%v)", client.ContextName, err))
					mutex.Unlock()
					return
				}
				mutex.Lock()
				for _, ds := range dsList.Items {
					clusterDistribution[client.ContextName]++
					namespaceDistribution[ds.Namespace]++
					readyStr := fmt.Sprintf("%d/%d", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
					nodeSelector := "None"
					if len(ds.Spec.Template.Spec.NodeSelector) > 0 {
						var selectors []string
						for k, v := range ds.Spec.Template.Spec.NodeSelector {
							selectors = append(selectors, fmt.Sprintf("%s=%s", k,v))
						}
						nodeSelector = strings.Join(selectors, ", ")
					}
					nodeSelectorDistribution[nodeSelector]++
					allDaemonSets = append(allDaemonSets, DaemonSetInfo{
						Cluster:   client.ContextName,
						Namespace: ds.Namespace,
						Name:      ds.Name,
						Ready:     readyStr,
						Node:      nodeSelector,
						Age:       formatAge(ds.CreationTimestamp),
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

		var nodeSelectorStats []NamespaceStat
		for n, c := range nodeSelectorDistribution {
			nodeSelectorStats = append(nodeSelectorStats, NamespaceStat{Name: n, Count: c})
		}
		sort.Slice(nodeSelectorStats, func(i, j int) bool {
			return nodeSelectorStats[i].Count > nodeSelectorStats[j].Count
		})
		if len(nodeSelectorStats) > topN {
			var otherSum int
			for _, o := range nodeSelectorStats[topN:] {
				otherSum += o.Count
			}
			nodeSelectorStats = nodeSelectorStats[:topN]
			nodeSelectorStats = append(nodeSelectorStats, NamespaceStat{Name: "Others", Count: otherSum})
		}

		data := DaemonSetPageData{
			PageBase:         base,
			DaemonSets:       allDaemonSets,
			TotalDaemonSets:  len(allDaemonSets),
			ClusterStats:     clusterStats,
			NamespaceStats:   namespaceStats,
			NodeSelectorStats: nodeSelectorStats,
		}
		return c.Render(200, "daemonsets.html", data)
	}
}

// handleGetDaemonSetDetail fetches a single daemonset
func handleGetDaemonSetDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		clusterContextName := c.QueryParam("cluster_name")
		namespace := c.QueryParam("namespace")
		dsName := c.QueryParam("name")
		if clusterContextName == "" || namespace == "" || dsName == "" {
			return c.String(400, "Missing required query parameters: cluster_name, namespace, name")
		}
		base := PageBase{
			Title:                dsName,
			ActivePage:           "daemonsets",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
		}
		clientset, err := findClient(pattern, clusterContextName)
		if err != nil {
			base.ErrorLogs = append(base.ErrorLogs, err.Error())
			return c.Render(200, "daemonset-detail.html", DaemonSetDetailPageData{PageBase: base})
		}
		data := DaemonSetDetailPageData{
			PageBase:       base,
			ClusterName:    clusterContextName,
			NamespaceName:  namespace,
			DaemonSetName:  dsName,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ds, err := clientset.AppsV1().DaemonSets(namespace).Get(ctx, dsName, metav1.GetOptions{})
		if err != nil {
			data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get daemonset: %v", err))
		} else {
			data.Overview.Status = fmt.Sprintf("%d/%d Ready", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
			data.Overview.Age = formatAge(ds.CreationTimestamp)
			nodeSelector := "None"
			if len(ds.Spec.Template.Spec.NodeSelector) > 0 {
				var selectors []string
				for k, v := range ds.Spec.Template.Spec.NodeSelector {
					selectors = append(selectors, fmt.Sprintf("%s=%s", k, v))
				}
				nodeSelector = strings.Join(selectors, ", ")
			}
			data.Overview.NodeSelector = nodeSelector
			selector, selErr := metav1.LabelSelectorAsSelector(ds.Spec.Selector)
			if selErr != nil {
				data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to build label selector: %v", selErr))
			} else {
				data.Overview.Selector = selector.String()
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
		fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", dsName, namespace)
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
		return c.Render(200, "daemonset-detail.html", data)
	}
}

// handleGetStatefulSets lists all statefulsets
func handleGetStatefulSets(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}
		base := PageBase{
			Title:                "All StatefulSets",
			ActivePage:           "statefulsets",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
		}
		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		var allStatefulSets []StatefulSetInfo
		clusterDistribution := make(map[string]int)
		namespaceDistribution := make(map[string]int)
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
				ssList, err := client.Clientset.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{})
				if err != nil {
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list statefulsets (%v)", client.ContextName, err))
					mutex.Unlock()
					return
				}
				mutex.Lock()
				for _, ss := range ssList.Items {
					clusterDistribution[client.ContextName]++
					namespaceDistribution[ss.Namespace]++
					var desiredReplicas int32 = 1
					if ss.Spec.Replicas != nil {
						desiredReplicas = *ss.Spec.Replicas
					}
					readyStr := fmt.Sprintf("%d/%d", ss.Status.ReadyReplicas, desiredReplicas)
					allStatefulSets = append(allStatefulSets, StatefulSetInfo{
						Cluster:   client.ContextName,
						Namespace: ss.Namespace,
						Name:      ss.Name,
						Ready:     readyStr,
						Age:       formatAge(ss.CreationTimestamp),
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

		data := StatefulSetPageData{
			PageBase:         base,
			StatefulSets:     allStatefulSets,
			TotalStatefulSets: len(allStatefulSets),
			ClusterStats:     clusterStats,
			NamespaceStats:   namespaceStats,
		}
		return c.Render(200, "statefulsets.html", data)
	}
}

// handleGetStatefulSetDetail fetches a single statefulset
func handleGetStatefulSetDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		clusterContextName := c.QueryParam("cluster_name")
		namespace := c.QueryParam("namespace")
		ssName := c.QueryParam("name")
		if clusterContextName == "" || namespace == "" || ssName == "" {
			return c.String(400, "Missing required query parameters: cluster_name, namespace, name")
		}
		base := PageBase{
			Title:                ssName,
			ActivePage:           "statefulsets",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
		}
		clientset, err := findClient(pattern, clusterContextName)
		if err != nil {
			base.ErrorLogs = append(base.ErrorLogs, err.Error())
			return c.Render(200, "statefulset-detail.html", StatefulSetDetailPageData{PageBase: base})
		}
		data := StatefulSetDetailPageData{
			PageBase:        base,
			ClusterName:     clusterContextName,
			NamespaceName:   namespace,
			StatefulSetName: ssName,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ss, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, ssName, metav1.GetOptions{})
		if err != nil {
			data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get statefulset: %v", err))
		} else {
			var desiredReplicas int32 = 1
			if ss.Spec.Replicas != nil {
				desiredReplicas = *ss.Spec.Replicas
			}
			data.Overview.Status = fmt.Sprintf("%d/%d Ready", ss.Status.ReadyReplicas, desiredReplicas)
			data.Overview.Age = formatAge(ss.CreationTimestamp)
			data.Overview.ServiceName = ss.Spec.ServiceName
			selector, selErr := metav1.LabelSelectorAsSelector(ss.Spec.Selector)
			if selErr != nil {
				data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to build label selector: %v", selErr))
			} else {
				data.Overview.Selector = selector.String()
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
		fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", ssName, namespace)
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
		return c.Render(200, "statefulset-detail.html", data)
	}
}