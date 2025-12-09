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
)

// handleGetWorkloadOverview provides a high-level dashboard of all resources.
func handleGetWorkloadOverview(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		// --- 1. Get base data ---
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}
		
		// [UPDATED] Injected IsAdmin
		base := PageBase{
			Title:                "Workload Overview",
			ActivePage:           "workload",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		var wg sync.WaitGroup
		var mutex sync.Mutex
		
		// Aggregators
		podStatusMap := make(map[string]int)
		// map[Namespace][Status] -> Count (For calculating heatmap color)
		podNamespaceStatusMap := make(map[string]map[string]int)
		podNamespaceCountMap := make(map[string]int)
		
		var depStat WorkloadStat
		var rsStat WorkloadStat
		var allWarnings []EventInfo
		var allRestarts []RestartingPodInfo
		var totalPods int
		reasonMap := make(map[string]int)

		if len(filesToProcess) == 0 {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("No clusters selected or found matching pattern '%s'", pattern))
		}

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		for _, client := range clients {
			wg.Add(1)
			go func(client KubeClient) {
				defer wg.Done()
				var subWg sync.WaitGroup
				subWg.Add(4) // Pods, Deployments, ReplicaSets, Events
				
				// 1. Pods
				go func() {
					defer subWg.Done()
					podList, err := client.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
					if err != nil {
						mutex.Lock()
						base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list pods (%v)", client.ContextName, err))
						mutex.Unlock()
						return
					}
					mutex.Lock()
					for _, pod := range podList.Items {
						totalPods++
						status := string(pod.Status.Phase)
						podStatusMap[status]++
						
						// Namespace Aggregation
						podNamespaceCountMap[pod.Namespace]++
						if _, ok := podNamespaceStatusMap[pod.Namespace]; !ok {
							podNamespaceStatusMap[pod.Namespace] = make(map[string]int)
						}
						podNamespaceStatusMap[pod.Namespace][status]++
						
						reason := getPodReason(pod)
						if reason != "" {
							reasonMap[reason]++
						}
						restartCount := 0
						for _, cs := range pod.Status.ContainerStatuses {
							restartCount += int(cs.RestartCount)
						}
						if restartCount > 0 {
							allRestarts = append(allRestarts, RestartingPodInfo{
								Cluster: client.ContextName, Namespace: pod.Namespace, Name: pod.Name, Restarts: restartCount, Reason: reason,
							})
						}
					}
					mutex.Unlock()
				}()
				
				// 2. Deployments
				go func() {
					defer subWg.Done()
					depList, err := client.Clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
					if err != nil {
						mutex.Lock(); base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list deployments (%v)", client.ContextName, err)); mutex.Unlock()
						return
					}
					mutex.Lock()
					for _, dep := range depList.Items {
						depStat.Total++
						var desiredReplicas int32 = 1; if dep.Spec.Replicas != nil { desiredReplicas = *dep.Spec.Replicas }
						if dep.Status.ReadyReplicas == desiredReplicas { depStat.Ready++ } else { depStat.NotReady++ }
					}
					mutex.Unlock()
				}()
				
				// 3. ReplicaSets
				go func() {
					defer subWg.Done()
					rsList, err := client.Clientset.AppsV1().ReplicaSets("").List(ctx, metav1.ListOptions{})
					if err != nil {
						mutex.Lock(); base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list replicasets (%v)", client.ContextName, err)); mutex.Unlock()
						return
					}
					mutex.Lock()
					for _, rs := range rsList.Items {
						rsStat.Total++
						var desiredReplicas int32 = 1; if rs.Spec.Replicas != nil { desiredReplicas = *rs.Spec.Replicas }
						if rs.Status.ReadyReplicas == desiredReplicas { rsStat.Ready++ } else { rsStat.NotReady++ }
					}
					mutex.Unlock()
				}()
				
				// 4. Events
				go func() {
					defer subWg.Done()
					eventList, err := client.Clientset.CoreV1().Events("").List(ctx, metav1.ListOptions{FieldSelector: "type=Warning"})
					if err != nil {
						mutex.Lock(); base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list events (%v)", client.ContextName, err)); mutex.Unlock()
						return
					}
					mutex.Lock()
					for _, e := range eventList.Items {
						allWarnings = append(allWarnings, EventInfo{
							Type: e.Type, Reason: e.Reason, Message: e.Message,
							Count: int(e.Count), LastSeen: formatAge(e.LastTimestamp),
						})
					}
					mutex.Unlock()
				}()
				subWg.Wait()
			}(client)
		}
		wg.Wait()

		// --- 5. Process Aggregated Data ---
		var podStatusSlice []PodStatusStat
		for status, count := range podStatusMap {
			podStatusSlice = append(podStatusSlice, PodStatusStat{Status: status, Count: count})
		}
		sort.Slice(podStatusSlice, func(i, j int) bool {
			return podStatusSlice[i].Count > podStatusSlice[j].Count
		})
		sort.Slice(allWarnings, func(i, j int) bool {
			return allWarnings[i].Count > allWarnings[j].Count
		})
		sort.Slice(allRestarts, func(i, j int) bool {
			return allRestarts[i].Restarts > allRestarts[j].Restarts
		})
		
		const topN = 10
		
		var reasonSlice []ReasonStat
		for reason, count := range reasonMap {
			reasonSlice = append(reasonSlice, ReasonStat{Reason: reason, Count: count})
		}
		sort.Slice(reasonSlice, func(i, j int) bool {
			return reasonSlice[i].Count > reasonSlice[j].Count
		})
		if len(reasonSlice) > topN {
			var otherSum int
			for _, r := range reasonSlice[topN:] {
				otherSum += r.Count
			}
			reasonSlice = reasonSlice[:topN]
			reasonSlice = append(reasonSlice, ReasonStat{Reason: "Others", Count: otherSum})
		}
		
		// --- Process Namespace Stats & Colors ---
		var nsSlice []NamespaceStat
		for ns, count := range podNamespaceCountMap {
			stats := podNamespaceStatusMap[ns]
			color := "#10b981" // Green
			
			// Build the Error Detail string
			var errorParts []string
			
			if c := stats["Failed"]; c > 0 {
				errorParts = append(errorParts, fmt.Sprintf("%d Failed", c))
				color = "#ef4444" // Red
			}
			if c := stats["Unknown"]; c > 0 {
				errorParts = append(errorParts, fmt.Sprintf("%d Unknown", c))
				color = "#ef4444" // Red
			}
			if c := stats["Pending"]; c > 0 {
				errorParts = append(errorParts, fmt.Sprintf("%d Pending", c))
				if color != "#ef4444" { color = "#f59e0b" } // Orange (unless already Red)
			}
			
			detail := ""
			if len(errorParts) > 0 {
				detail = strings.Join(errorParts, ", ")
			}
			
			nsSlice = append(nsSlice, NamespaceStat{
				Name:        ns, 
				Count:       count, 
				Color:       color,
				ErrorDetail: detail,
			})
		}
		sort.Slice(nsSlice, func(i, j int) bool {
			return nsSlice[i].Count > nsSlice[j].Count
		})
		if len(nsSlice) > 30 {
			nsSlice = nsSlice[:30]
		}
		
		if len(allWarnings) > 20 {
			allWarnings = allWarnings[:20]
		}
		if len(allRestarts) > 20 {
			allRestarts = allRestarts[:20]
		}

		data := WorkloadOverviewPageData{
			PageBase:          base,
			PodStatus:         podStatusSlice,
			PodStatusTotal:    totalPods,
			DeploymentStatus:  depStat,
			ReplicaSetStatus:  rsStat,
			RecentWarnings:    allWarnings,
			RecentRestarts:    allRestarts,
			PodReasonStats:    reasonSlice,
			PodNamespaceStats: nsSlice,
		}
		return c.Render(200, "workload-overview.html", data)
	}
}