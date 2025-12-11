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
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}
		
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
		
		if len(filesToProcess) == 0 {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("No clusters selected or found matching pattern '%s'", pattern))
		}

		// --- 1. Define Fetch Logic ---
		// This struct holds the pre-calculated stats for a SINGLE cluster
		type workloadFetchResult struct {
			ClusterName      string
			TotalPods        int
			PodStatus        map[string]int            // Status -> Count
			PodNsStatus      map[string]map[string]int // Namespace -> Status -> Count
			PodNsCount       map[string]int            // Namespace -> Total Count
			Reasons          map[string]int            // Reason -> Count
			Restarts         []RestartingPodInfo
			Warnings         []EventInfo
			DeploymentStatus WorkloadStat
			ReplicaSetStatus WorkloadStat
		}

		fetchClusterWorkload := func(client KubeClient) (workloadFetchResult, error) {
			res := workloadFetchResult{
				ClusterName: client.ContextName,
				PodStatus:   make(map[string]int),
				PodNsStatus: make(map[string]map[string]int),
				PodNsCount:  make(map[string]int),
				Reasons:     make(map[string]int),
			}

			// We use internal concurrency here to fetch the 4 resource types simultaneously
			// for this specific cluster.
			var subWg sync.WaitGroup
			var subMutex sync.Mutex
			subWg.Add(4)

			// 1. Pods
			go func() {
				defer subWg.Done()
				podList, err := client.Clientset.CoreV1().Pods("").List(context.Background(), metav1.ListOptions{})
				if err != nil { return }

				subMutex.Lock()
				defer subMutex.Unlock()

				for _, pod := range podList.Items {
					res.TotalPods++
					status := string(pod.Status.Phase)
					res.PodStatus[status]++

					// Namespace Stats
					res.PodNsCount[pod.Namespace]++
					if _, ok := res.PodNsStatus[pod.Namespace]; !ok {
						res.PodNsStatus[pod.Namespace] = make(map[string]int)
					}
					res.PodNsStatus[pod.Namespace][status]++

					// Reasons
					reason := getPodReason(pod)
					if reason != "" {
						res.Reasons[reason]++
					}

					// Restarts
					restartCount := 0
					for _, cs := range pod.Status.ContainerStatuses {
						restartCount += int(cs.RestartCount)
					}
					if restartCount > 0 {
						res.Restarts = append(res.Restarts, RestartingPodInfo{
							Cluster:   client.ContextName,
							Namespace: pod.Namespace,
							Name:      pod.Name,
							Restarts:  restartCount,
							Reason:    reason,
						})
					}
				}
			}()

			// 2. Deployments
			go func() {
				defer subWg.Done()
				depList, err := client.Clientset.AppsV1().Deployments("").List(context.Background(), metav1.ListOptions{})
				if err != nil { return }
				
				subMutex.Lock()
				defer subMutex.Unlock()

				for _, dep := range depList.Items {
					res.DeploymentStatus.Total++
					var desired int32 = 1
					if dep.Spec.Replicas != nil { desired = *dep.Spec.Replicas }
					if dep.Status.ReadyReplicas == desired {
						res.DeploymentStatus.Ready++
					} else {
						res.DeploymentStatus.NotReady++
					}
				}
			}()

			// 3. ReplicaSets
			go func() {
				defer subWg.Done()
				rsList, err := client.Clientset.AppsV1().ReplicaSets("").List(context.Background(), metav1.ListOptions{})
				if err != nil { return }

				subMutex.Lock()
				defer subMutex.Unlock()

				for _, rs := range rsList.Items {
					res.ReplicaSetStatus.Total++
					var desired int32 = 1
					if rs.Spec.Replicas != nil { desired = *rs.Spec.Replicas }
					if rs.Status.ReadyReplicas == desired {
						res.ReplicaSetStatus.Ready++
					} else {
						res.ReplicaSetStatus.NotReady++
					}
				}
			}()

			// 4. Events (Warnings)
			go func() {
				defer subWg.Done()
				eventList, err := client.Clientset.CoreV1().Events("").List(context.Background(), metav1.ListOptions{FieldSelector: "type=Warning"})
				if err != nil { return }

				subMutex.Lock()
				defer subMutex.Unlock()

				for _, e := range eventList.Items {
					res.Warnings = append(res.Warnings, EventInfo{
						Type: e.Type, Reason: e.Reason, Message: e.Message,
						Count: int(e.Count), LastSeen: formatAge(e.LastTimestamp),
					})
				}
			}()

			subWg.Wait()
			return res, nil
		}

		// --- 2. Execute ---
		results, fetchErrors := ParallelFetch(clients, fetchClusterWorkload)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		// --- 3. Aggregate Results ---
		podStatusMap := make(map[string]int)
		podNamespaceStatusMap := make(map[string]map[string]int)
		podNamespaceCountMap := make(map[string]int)
		reasonMap := make(map[string]int)
		
		var depStat WorkloadStat
		var rsStat WorkloadStat
		var allWarnings []EventInfo
		var allRestarts []RestartingPodInfo
		var totalPods int

		for _, res := range results {
			totalPods += res.TotalPods
			
			// Merge Maps
			for k, v := range res.PodStatus { podStatusMap[k] += v }
			for k, v := range res.PodNsCount { podNamespaceCountMap[k] += v }
			for k, v := range res.Reasons { reasonMap[k] += v }
			
			// Deep Merge Namespace Status
			for ns, statusMap := range res.PodNsStatus {
				if _, ok := podNamespaceStatusMap[ns]; !ok {
					podNamespaceStatusMap[ns] = make(map[string]int)
				}
				for status, count := range statusMap {
					podNamespaceStatusMap[ns][status] += count
				}
			}

			// Merge Lists & Stats
			allRestarts = append(allRestarts, res.Restarts...)
			allWarnings = append(allWarnings, res.Warnings...)
			
			depStat.Total += res.DeploymentStatus.Total
			depStat.Ready += res.DeploymentStatus.Ready
			depStat.NotReady += res.DeploymentStatus.NotReady
			
			rsStat.Total += res.ReplicaSetStatus.Total
			rsStat.Ready += res.ReplicaSetStatus.Ready
			rsStat.NotReady += res.ReplicaSetStatus.NotReady
		}

		// --- 4. Sort and Prepare View Data ---
		var podStatusSlice []PodStatusStat
		for status, count := range podStatusMap {
			podStatusSlice = append(podStatusSlice, PodStatusStat{Status: status, Count: count})
		}
		sort.Slice(podStatusSlice, func(i, j int) bool { return podStatusSlice[i].Count > podStatusSlice[j].Count })
		
		sort.Slice(allWarnings, func(i, j int) bool { return allWarnings[i].Count > allWarnings[j].Count })
		sort.Slice(allRestarts, func(i, j int) bool { return allRestarts[i].Restarts > allRestarts[j].Restarts })
		
		const topN = 10
		var reasonSlice []ReasonStat
		for reason, count := range reasonMap {
			reasonSlice = append(reasonSlice, ReasonStat{Reason: reason, Count: count})
		}
		sort.Slice(reasonSlice, func(i, j int) bool { return reasonSlice[i].Count > reasonSlice[j].Count })
		if len(reasonSlice) > topN {
			var otherSum int; for _, r := range reasonSlice[topN:] { otherSum += r.Count }; reasonSlice = reasonSlice[:topN]; reasonSlice = append(reasonSlice, ReasonStat{Reason: "Others", Count: otherSum})
		}
		
		// Process Namespace Heatmap Colors
		var nsSlice []NamespaceStat
		for ns, count := range podNamespaceCountMap {
			stats := podNamespaceStatusMap[ns]
			color := "#10b981" // Green
			
			var errorParts []string
			if c := stats["Failed"]; c > 0 { errorParts = append(errorParts, fmt.Sprintf("%d Failed", c)); color = "#ef4444" }
			if c := stats["Unknown"]; c > 0 { errorParts = append(errorParts, fmt.Sprintf("%d Unknown", c)); color = "#ef4444" }
			if c := stats["Pending"]; c > 0 {
				errorParts = append(errorParts, fmt.Sprintf("%d Pending", c))
				if color != "#ef4444" { color = "#f59e0b" }
			}
			
			detail := ""
			if len(errorParts) > 0 { detail = strings.Join(errorParts, ", ") }
			
			nsSlice = append(nsSlice, NamespaceStat{ Name: ns, Count: count, Color: color, ErrorDetail: detail })
		}
		sort.Slice(nsSlice, func(i, j int) bool { return nsSlice[i].Count > nsSlice[j].Count })
		if len(nsSlice) > 30 { nsSlice = nsSlice[:30] }
		
		if len(allWarnings) > 20 { allWarnings = allWarnings[:20] }
		if len(allRestarts) > 20 { allRestarts = allRestarts[:20] }

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