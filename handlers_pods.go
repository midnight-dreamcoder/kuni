package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// handleGetPods lists all pods from all clusters
func handleGetPods(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		
		// FIXED: Use getConfigsToProcess (Hybrid Loader)
		configsToProcess, err := getConfigsToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding configs")
		}
		
		base := PageBase{
			Title:                "All Pods",
			ActivePage:           "pods",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		// FIXED: Pass []ClusterConfig
		clients, clientErrors := createClients(configsToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)

		if len(configsToProcess) == 0 {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("No clusters selected or found matching pattern '%s'", pattern))
		}

		// --- 1. Define the Fetch Logic (Single Cluster) ---
		fetchPods := func(client KubeClient) ([]PodInfo, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			podList, err := client.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, fmt.Errorf("failed to list pods: %v", err)
			}

			var localPods []PodInfo
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

				localPods = append(localPods, PodInfo{
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
			return localPods, nil
		}

		// --- 2. Execute via ParallelFetch ---
		results, fetchErrors := ParallelFetch(clients, fetchPods)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		// --- 3. Aggregate Results ---
		var allPods []PodInfo
		clusterDistribution := make(map[string]int)
		namespaceDistribution := make(map[string]int)
		podStatusMap := make(map[string]int)
		reasonMap := make(map[string]int)

		for _, clusterPods := range results {
			for _, pod := range clusterPods {
				allPods = append(allPods, pod)
				
				// Update Stats
				clusterDistribution[pod.Cluster]++
				namespaceDistribution[pod.Namespace]++
				podStatusMap[pod.Status]++
				if pod.Reason != "" {
					reasonMap[pod.Reason]++
				}
			}
		}

		// --- 4. Sort and Format Data for View ---
		var clusterStats []ClusterStat
		for n, c := range clusterDistribution { 
			clusterStats = append(clusterStats, ClusterStat{Name: n, Count: c}) 
		}
		sort.Slice(clusterStats, func(i, j int) bool { return clusterStats[i].Name < clusterStats[j].Name })
		
		var namespaceStats []NamespaceStat
		for n, c := range namespaceDistribution {
			namespaceStats = append(namespaceStats, NamespaceStat{Name: n, Count: c})
		}
		sort.Slice(namespaceStats, func(i, j int) bool {
			return namespaceStats[i].Count > namespaceStats[j].Count
		})
		const topN = 10
		if len(namespaceStats) > topN {
			var otherSum int
			for _, ns := range namespaceStats[topN:] {
				otherSum += ns.Count
			}
			namespaceStats = namespaceStats[:topN]
			namespaceStats = append(namespaceStats, NamespaceStat{Name: "Others", Count: otherSum})
		}

		var podStatusSlice []PodStatusStat
		for status, count := range podStatusMap {
			podStatusSlice = append(podStatusSlice, PodStatusStat{Status: status, Count: count})
		}
		sort.Slice(podStatusSlice, func(i, j int) bool {
			return podStatusSlice[i].Count > podStatusSlice[j].Count
		})
		
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

		data := PodPageData{
			PageBase:       base,
			Pods:           allPods,
			TotalPods:      len(allPods),
			ClusterStats:   clusterStats,
			NamespaceStats: namespaceStats,
			PodStatusStats: podStatusSlice,
			PodReasonStats: reasonSlice,
		}
		return c.Render(200, "pods.html", data)
	}
}

// handleGetPodDetail (Uses findClient, which is already fixed in helpers.go)
func handleGetPodDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		clusterContextName := c.QueryParam("cluster_name")
		namespace := c.QueryParam("namespace")
		podName := c.QueryParam("name")
		if clusterContextName == "" || namespace == "" || podName == "" {
			return c.String(400, "Missing required query parameters: cluster_name, namespace, name")
		}

		base := PageBase{
			Title:                podName,
			ActivePage:           "pods",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clientset, err := findClient(pattern, clusterContextName)
		if err != nil {
			base.ErrorLogs = append(base.ErrorLogs, err.Error())
			return c.Render(200, "pod-detail.html", PodDetailPageData{PageBase: base})
		}
		
		data := PodDetailPageData{
			PageBase:      base,
			ClusterName:   clusterContextName,
			NamespaceName: namespace,
			PodName:       podName,
		}
		
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get pod: %v", err))
			} else {
				data.Status = string(pod.Status.Phase)
				data.Reason = getPodReason(*pod)
				data.Node = pod.Spec.NodeName
				data.PodIP = pod.Status.PodIP
				data.QoS = string(pod.Status.QOSClass)
				data.ServiceAccount = pod.Spec.ServiceAccountName
				data.Age = formatAge(pod.CreationTimestamp)
				data.Labels = pod.ObjectMeta.Labels
				data.Annotations = pod.ObjectMeta.Annotations
				data.NodeSelector = pod.Spec.NodeSelector
				for _, t := range pod.Spec.Tolerations {
					data.Tolerations = append(data.Tolerations, fmt.Sprintf("%s (%s)", t.Key, t.Effect))
				}
				if len(pod.OwnerReferences) > 0 {
					data.OwnerName = pod.OwnerReferences[0].Name
					data.OwnerKind = pod.OwnerReferences[0].Kind
				} else {
					data.OwnerName = "" // No owner
				}

				statusMap := make(map[string]v1.ContainerStatus)
				for _, s := range pod.Status.ContainerStatuses {
					statusMap[s.Name] = s
				}
				
				for _, c := range pod.Spec.InitContainers {
					data.InitContainers = append(data.InitContainers, parseContainer(c))
				}
				for _, c := range pod.Spec.Containers {
					info := parseContainer(c)
					if status, ok := statusMap[c.Name]; ok {
						info.Ready = status.Ready
						info.RestartCount = int(status.RestartCount)
						if status.State.Running != nil {
							info.State = "Running"
						} else if status.State.Terminated != nil {
							info.State = "Terminated"
							info.Reason = status.State.Terminated.Reason
						} else if status.State.Waiting != nil {
							info.State = "Waiting"
							info.Reason = status.State.Waiting.Reason
						}
					}
					data.Containers = append(data.Containers, info)
				}
				for _, c := range pod.Status.Conditions {
					data.Conditions = append(data.Conditions, PodConditionInfo{
						Type: string(c.Type), Status: string(c.Status),
						Reason: c.Reason, Message: c.Message,
					})
				}
				for _, v := range pod.Spec.Volumes {
					volType := "Unknown"
					if v.ConfigMap != nil { volType = "ConfigMap" }
					if v.Secret != nil { volType = "Secret" }
					if v.EmptyDir != nil { volType = "EmptyDir" }
					if v.PersistentVolumeClaim != nil { volType = "PersistentVolumeClaim" }
					data.Volumes = append(data.Volumes, VolumeInfo{
						Name: v.Name, Type: volType,
					})
				}
			}
		}()
		
		go func() {
			defer wg.Done()
			fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", podName, namespace)
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
		}()

		wg.Wait()
		return c.Render(200, "pod-detail.html", data)
	}
}

// handleGetPodLogs streams logs for a specific container
func handleGetPodLogs(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		if !CurrentConfig.IsAdmin {
			return c.String(http.StatusForbidden, "⛔ Access Denied: Pod logs contain sensitive information.")
		}

		clusterName := c.QueryParam("cluster_name")
		namespace := c.QueryParam("namespace")
		podName := c.QueryParam("name")
		containerName := c.QueryParam("container")

		clientset, err := findClient(pattern, clusterName)
		if err != nil {
			return c.String(http.StatusInternalServerError, "Client error: "+err.Error())
		}
		
		logCtx, logCancel := context.WithTimeout(context.Background(), 1*time.Hour)
		defer logCancel()

		logOptions := &v1.PodLogOptions{
			Container: containerName,
			Follow:    true,
			TailLines: func() *int64 { i := int64(1000); return &i }(),
		}
		
		req := clientset.CoreV1().Pods(namespace).GetLogs(podName, logOptions)
		
		podLogs, err := req.Stream(logCtx) 
		if err != nil {
			return c.String(http.StatusNotFound, "Error starting log stream: "+err.Error())
		}
		defer podLogs.Close()
		
		c.Response().Header().Set(echo.HeaderContentType, echo.MIMETextPlainCharsetUTF8)
		c.Response().Header().Set("X-Content-Type-Options", "nosniff")
		c.Response().Header().Set("Cache-Control", "no-cache") 
		c.Response().WriteHeader(http.StatusOK) 
		c.Response().Flush()

		buf := make([]byte, 1024)
		for {
			n, readErr := podLogs.Read(buf)
			if n > 0 {
				_, writeErr := c.Response().Write(buf[:n])
				if writeErr != nil {
					break
				}
				c.Response().Flush()
			}
			
			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				log.Printf("Stream error: %v", readErr)
				break
			}
		}
		
		return nil
	}
}