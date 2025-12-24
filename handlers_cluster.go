package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes" 
)

// handleGetClusters renders the list immediately
func handleGetClusters(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Use new Helper for PageBase (Handles Auth & Filter)
		base := GetBaseData(c, "Cluster Status", "clusters")

		if errParam := c.QueryParam("error"); errParam != "" {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("❌ Error: %s", errParam))
		}

		configs, err := LoadAllConfigs(pattern)
		if err != nil {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Error loading configs: %v", err))
		}

		selectedQuery := c.QueryParams()["c"]
		selectedClustersMap := make(map[string]bool)
		for _, s := range selectedQuery {
			selectedClustersMap[s] = true
		}
		
		// REMOVED: base.SelectedClusters = selectedClustersMap (Invalid field on PageBase)

		var clusters []ClusterInfo
		for _, cfg := range configs {
			clusters = append(clusters, ClusterInfo{
				Name:       cfg.ContextName,
				ConfigName: cfg.Name, 
				Status:     "Pending", 
				Latency:    "-",
				Version:    "-",
				Provider:   "...",
				NodeCount:  0,
			})
		}
		sort.Slice(clusters, func(i, j int) bool { return clusters[i].Name < clusters[j].Name })

		data := ClusterPageData{
			PageBase:         base,
			Clusters:         clusters,
			SelectedClusters: selectedClustersMap,
		}
		return c.Render(200, "clusters.html", data)
	}
}

// handleGetClusterStatusAPI checks a single cluster's connectivity
func handleGetClusterStatusAPI(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		clusterName := c.QueryParam("name")
		if clusterName == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Missing name"})
		}

		config, err := findConfigWithTimeout(pattern, clusterName, 5*time.Second)
		if err != nil {
			return c.JSON(http.StatusOK, ClusterInfo{Status: "Offline", Latency: "Error", Provider: "Unknown"})
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			return c.JSON(http.StatusOK, ClusterInfo{Status: "Offline", Latency: "Config Error", Provider: "Unknown"})
		}

		start := time.Now()
		versionInfo, err := clientset.Discovery().ServerVersion()
		latency := time.Since(start)
		
		info := ClusterInfo{
			Name:    clusterName,
			Status:  "Online",
			Latency: fmt.Sprintf("%d ms", latency.Milliseconds()),
		}

		if err != nil {
			info.Status = "Offline"
			return c.JSON(http.StatusOK, info)
		}

		var typedVersion *k8sversion.Info
		typedVersion = versionInfo
		info.Version = typedVersion.GitVersion
		
		if strings.Contains(info.Version, "-eks-") {
			info.Provider = "AWS (EKS)"
		} else if strings.Contains(info.Version, "-gke") {
			info.Provider = "GCP (GKE)"
		} else {
			info.Provider = "On-Prem"
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		fullList, _ := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if fullList != nil {
			info.NodeCount = len(fullList.Items)
			if info.NodeCount > 0 {
				pID := fullList.Items[0].Spec.ProviderID
				if strings.HasPrefix(pID, "aws") {
					info.Provider = "AWS"
				} else if strings.HasPrefix(pID, "gce") {
					info.Provider = "GCP"
				} else if strings.HasPrefix(pID, "azure") {
					info.Provider = "Azure"
				}
			}
		}
		
		if config.Host != "" {
			info.ApiServer = strings.TrimPrefix(strings.TrimPrefix(config.Host, "https://"), "http://")
		}

		return c.JSON(http.StatusOK, info)
	}
}

// handleUploadConfig (Protected by middleware in main.go)
func handleUploadConfig(kubeDir string) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Double check admin status
		isAdmin, _ := c.Get("isAdmin").(bool)
		if !isAdmin {
			return c.Redirect(302, "/clusters?error=upload_not_allowed_in_guest_mode")
		}

		file, err := c.FormFile("kubeconfig")
		if err != nil {
			return c.Redirect(302, "/clusters?error=upload_failed")
		}

		src, err := file.Open()
		if err != nil {
			return c.Redirect(302, "/clusters?error=upload_read_failed")
		}
		defer src.Close()

		filename := fmt.Sprintf("config-ops-upload-%d.yaml", time.Now().Unix())
		dstPath := filepath.Join(kubeDir, filename)

		dst, err := os.Create(dstPath)
		if err != nil {
			return c.Redirect(302, "/clusters?error=upload_save_failed")
		}
		defer dst.Close()

		if _, err = io.Copy(dst, src); err != nil {
			return c.Redirect(302, "/clusters?error=upload_copy_failed")
		}

		return c.Redirect(302, "/clusters")
	}
}

// handleGetClusterDetail fetches all details for a single cluster
func handleGetClusterDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		clusterContextName := c.QueryParam("cluster_name")
		if clusterContextName == "" {
			return c.String(400, "Missing required query parameter: cluster_name")
		}

		// Use new Helper
		base := GetBaseData(c, clusterContextName, "clusters")

		clientset, err := findClient(pattern, clusterContextName)
		if err != nil {
			base.ErrorLogs = append(base.ErrorLogs, err.Error())
			return c.Render(200, "cluster-detail.html", ClusterDetailPageData{PageBase: base})
		}
		
		data := ClusterDetailPageData{
			PageBase:   base,
			Namespaces: make([]string, 0),
			Nodes:      make([]NodeInfo, 0),
			Events:     make([]EventInfo, 0),
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var wg sync.WaitGroup
		var mutex sync.Mutex
		wg.Add(5)

		var totalCapCpu, totalAllocCpu, totalUsageCpu resource.Quantity
		var totalCapMem, totalAllocMem, totalUsageMem resource.Quantity

		go func() {
			defer wg.Done()
			start := time.Now()
			versionInfo, err := clientset.Discovery().ServerVersion()
			latency := time.Since(start)

			data.Cluster.Name = clusterContextName
			data.Cluster.ApiServer = clientset.Discovery().RESTClient().Get().URL().Host
			if err != nil {
				data.Cluster.Status = "Offline"
				data.Cluster.Latency = "N/A"
			} else {
				data.Cluster.Status = "Online"
				data.Cluster.Latency = fmt.Sprintf("%d ms", latency.Milliseconds())
				data.Cluster.Version = versionInfo.GitVersion
			}
		}()

		go func() {
			defer wg.Done()
			var nodes []NodeInfo
			nodeList, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			if err != nil {
				mutex.Lock()
				data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to list nodes: %v", err))
				mutex.Unlock()
				return
			}
			data.Cluster.NodeCount = len(nodeList.Items)
			for _, node := range nodeList.Items {
				status, reason := getNodeStatus(node)
				schedulable := "Enabled"
				if node.Spec.Unschedulable {
					schedulable = "Disabled"
				}
				role := "Worker"
				if _, ok := node.Labels["node-role.kubernetes.io/control-plane"]; ok {
					role = "Control-Plane"
				} else if _, ok := node.Labels["node-role.kubernetes.io/master"]; ok {
					role = "Control-Plane"
				} else if val, ok := node.Labels["kubernetes.io/role"]; ok {
					if val == "master" || val == "control-plane" {
						role = "Control-Plane"
					}
				}
				nodes = append(nodes, NodeInfo{
					Cluster:     clusterContextName,
					Name:        node.Name,
					Status:      status,
					Reason:      reason,
					Role:        role,
					Schedulable: schedulable,
					Kubelet:     node.Status.NodeInfo.KubeletVersion,
					Runtime:     node.Status.NodeInfo.ContainerRuntimeVersion,
					CpuAlloc:    node.Status.Allocatable.Cpu().String(),
					MemAlloc:    formatMemory(node.Status.Allocatable.Memory()),
					Taints:      len(node.Spec.Taints),
				})
				mutex.Lock()
				totalCapCpu.Add(*node.Status.Capacity.Cpu())
				totalAllocCpu.Add(*node.Status.Allocatable.Cpu())
				totalCapMem.Add(*node.Status.Capacity.Memory())
				totalAllocMem.Add(*node.Status.Allocatable.Memory())
				mutex.Unlock()
			}
			data.Nodes = nodes
		}()

		go func() {
			defer wg.Done()
			nsList, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
			if err != nil {
				mutex.Lock()
				data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to list namespaces: %v", err))
				mutex.Unlock()
				return
			}
			var nsNames []string
			for _, ns := range nsList.Items {
				nsNames = append(nsNames, ns.Name)
			}
			sort.Strings(nsNames)
			data.Namespaces = nsNames
		}()

		go func() {
			defer wg.Done()
			eventFieldSelector := "involvedObject.kind=Node"
			eventList, eventErr := clientset.CoreV1().Events("").List(ctx, metav1.ListOptions{FieldSelector: eventFieldSelector, Limit: 50})
			if err != nil {
				mutex.Lock()
				data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get events: %v", eventErr))
				mutex.Unlock()
				return
			}
			sort.Slice(eventList.Items, func(i, j int) bool {
				return eventList.Items[i].LastTimestamp.Time.After(eventList.Items[j].LastTimestamp.Time)
			})
			for _, e := range eventList.Items {
				data.Events = append(data.Events, EventInfo{
					Type: e.Type, Reason: e.Reason, Message: e.Message,
					Count: int(e.Count), LastSeen: formatAge(e.LastTimestamp),
				})
			}
		}()

		go func() {
			defer wg.Done()
			metricsClient, metricsErr := findMetricsClient(pattern, clusterContextName)
			if metricsErr != nil {
				return
			}
			metricsList, err := metricsClient.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
			if err != nil {
				return
			}
			mutex.Lock()
			for _, metrics := range metricsList.Items {
				totalUsageCpu.Add(*metrics.Usage.Cpu())
				totalUsageMem.Add(*metrics.Usage.Memory())
			}
			mutex.Unlock()
		}()

		wg.Wait()

		data.Resources.CapacityCpu = formatCpu(&totalCapCpu)
		data.Resources.AllocatableCpu = formatCpu(&totalAllocCpu)
		data.Resources.UsageCpu = formatCpu(&totalUsageCpu)
		data.Resources.CapacityMem = formatMemory(&totalCapMem)
		data.Resources.AllocatableMem = formatMemory(&totalAllocMem)
		data.Resources.UsageMem = formatMemory(&totalUsageMem)

		if totalCapCpu.MilliValue() > 0 {
			data.Resources.AllocatableCpuPercent = (float64(totalAllocCpu.MilliValue()) / float64(totalCapCpu.MilliValue())) * 100.0
			data.Resources.UsageCpuPercent = (float64(totalUsageCpu.MilliValue()) / float64(totalCapCpu.MilliValue())) * 100.0
		}
		if totalCapMem.Value() > 0 {
			data.Resources.AllocatableMemPercent = (float64(totalAllocMem.Value()) / float64(totalCapMem.Value())) * 100.0
			data.Resources.UsageMemPercent = (float64(totalUsageMem.Value()) / float64(totalCapMem.Value())) * 100.0
		}

		return c.Render(200, "cluster-detail.html", data)
	}
}

// handleGetClusterOverview provides a high-level dashboard
func handleGetClusterOverview(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Use Helper
		base := GetBaseData(c, "Cluster Overview", "overview")
		
		configsToProcess, err := getConfigsToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding configs")
		}

		clients, clientErrors := createClients(configsToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		
		if len(configsToProcess) == 0 {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("No clusters selected or found matching pattern '%s'", pattern))
		}

		type clusterOverviewResult struct {
			ClusterName string
			NodeStat    WorkloadStat
			PVStat      map[string]int
			TotalPVs    int
			Summary     ResourceSummary
		}

		fetchOverview := func(client KubeClient) (clusterOverviewResult, error) {
			result := clusterOverviewResult{
				ClusterName: client.ContextName,
				PVStat:      make(map[string]int),
			}
			
			var clusterCapCpu, clusterAllocCpu, clusterUsageCpu resource.Quantity
			var clusterCapMem, clusterAllocMem, clusterUsageMem resource.Quantity
			var subWg sync.WaitGroup
			var subMutex sync.Mutex
			
			subWg.Add(3)

			go func() {
				defer subWg.Done()
				nodeList, err := client.Clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
				if err != nil { return }
				
				subMutex.Lock()
				defer subMutex.Unlock()
				
				for _, node := range nodeList.Items {
					result.NodeStat.Total++
					status, _ := getNodeStatus(node)
					if status == "Ready" {
						result.NodeStat.Ready++
					} else {
						result.NodeStat.NotReady++
					}
					clusterCapCpu.Add(*node.Status.Capacity.Cpu())
					clusterAllocCpu.Add(*node.Status.Allocatable.Cpu())
					clusterCapMem.Add(*node.Status.Capacity.Memory())
					clusterAllocMem.Add(*node.Status.Allocatable.Memory())
				}
			}()

			go func() {
				defer subWg.Done()
				pvList, err := client.Clientset.CoreV1().PersistentVolumes().List(context.Background(), metav1.ListOptions{})
				if err != nil { return }
				
				subMutex.Lock()
				defer subMutex.Unlock()
				
				result.TotalPVs = len(pvList.Items)
				for _, pv := range pvList.Items {
					result.PVStat[string(pv.Status.Phase)]++
				}
			}()

			go func() {
				defer subWg.Done()
				metricsClient, err := findMetricsClient(pattern, client.ContextName)
				if err != nil { return }
				metricsList, err := metricsClient.MetricsV1beta1().NodeMetricses().List(context.Background(), metav1.ListOptions{})
				if err != nil { return }
				
				subMutex.Lock()
				defer subMutex.Unlock()
				
				for _, metrics := range metricsList.Items {
					clusterUsageCpu.Add(*metrics.Usage.Cpu())
					clusterUsageMem.Add(*metrics.Usage.Memory())
				}
			}()

			subWg.Wait()

			result.Summary.CapacityCpu = formatCpu(&clusterCapCpu)
			result.Summary.AllocatableCpu = formatCpu(&clusterAllocCpu)
			result.Summary.UsageCpu = formatCpu(&clusterUsageCpu)
			result.Summary.CapacityMem = formatMemory(&clusterCapMem)
			result.Summary.AllocatableMem = formatMemory(&clusterAllocMem)
			result.Summary.UsageMem = formatMemory(&clusterUsageMem)

			if clusterCapCpu.MilliValue() > 0 {
				result.Summary.AllocatableCpuPercent = (float64(clusterAllocCpu.MilliValue()) / float64(clusterCapCpu.MilliValue())) * 100.0
				result.Summary.UsageCpuPercent = (float64(clusterUsageCpu.MilliValue()) / float64(clusterCapCpu.MilliValue())) * 100.0
			}
			if clusterCapMem.Value() > 0 {
				result.Summary.AllocatableMemPercent = (float64(clusterAllocMem.Value()) / float64(clusterCapMem.Value())) * 100.0
				result.Summary.UsageMemPercent = (float64(clusterUsageMem.Value()) / float64(clusterCapMem.Value())) * 100.0
			}

			return result, nil
		}

		results, fetchErrors := ParallelFetch(clients, fetchOverview)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		var nodeStat WorkloadStat
		pvStatusMap := make(map[string]int)
		var totalPVs int
		resourceMap := make(map[string]ResourceSummary)
		var clusterNames []string

		for _, res := range results {
			nodeStat.Total += res.NodeStat.Total
			nodeStat.Ready += res.NodeStat.Ready
			nodeStat.NotReady += res.NodeStat.NotReady
			totalPVs += res.TotalPVs
			for k, v := range res.PVStat {
				pvStatusMap[k] += v
			}
			resourceMap[res.ClusterName] = res.Summary
			clusterNames = append(clusterNames, res.ClusterName)
		}

		var pvStatusSlice []PodStatusStat
		for status, count := range pvStatusMap {
			pvStatusSlice = append(pvStatusSlice, PodStatusStat{Status: status, Count: count})
		}
		sort.Slice(pvStatusSlice, func(i, j int) bool { return pvStatusSlice[i].Count > pvStatusSlice[j].Count })
		sort.Strings(clusterNames)

		data := ClusterOverviewPageData{
			PageBase:         base,
			TotalClusters:    len(clients),
			NodeStatus:       nodeStat,
			PVStatus:         pvStatusSlice,
			TotalPVs:         totalPVs,
			ClusterResources: resourceMap,
			ClusterNames:     clusterNames,
		}
		return c.Render(200, "overview.html", data)
	}
}

// handleGetNodes lists all nodes from all clusters
func handleGetNodes(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Use Helper
		base := GetBaseData(c, "All Nodes", "nodes")
		
		configsToProcess, err := getConfigsToProcess(c, pattern)
		if err != nil { return c.String(500, "Error finding configs") }
		
		clients, _ := createClients(configsToProcess)
		
		type nodeFetchResult struct { ClusterName string; Items []NodeInfo }
		fetchNodes := func(client KubeClient) (nodeFetchResult, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			list, err := client.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			if err != nil { return nodeFetchResult{}, err }
			var nodes []NodeInfo
			for _, node := range list.Items {
				status, reason := getNodeStatus(node)
				sched := "Enabled"; if node.Spec.Unschedulable { sched = "Disabled" }
				nodes = append(nodes, NodeInfo{Cluster: client.ContextName, Name: node.Name, Status: status, Reason: reason, Schedulable: sched, Kubelet: node.Status.NodeInfo.KubeletVersion})
			}
			return nodeFetchResult{ClusterName: client.ContextName, Items: nodes}, nil
		}
		results, _ := ParallelFetch(clients, fetchNodes)
		var allNodes []NodeInfo; cDist := make(map[string]int)
		for _, res := range results { for _, n := range res.Items { allNodes = append(allNodes, n); cDist[res.ClusterName]++ } }
		
		var cStats []ClusterStat; for k, v := range cDist { cStats = append(cStats, ClusterStat{Name: k, Count: v}) }; sort.Slice(cStats, func(i, j int) bool { return cStats[i].Name < cStats[j].Name })
		return c.Render(200, "nodes.html", NodePageData{PageBase: base, Nodes: allNodes, TotalNodes: len(allNodes), ClusterStats: cStats})
	}
}

// handleGetNodeDetail fetches a single node and its pods/events
func handleGetNodeDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		clusterContextName := c.QueryParam("cluster_name")
		nodeName := c.QueryParam("name")
		
		// Use Helper
		base := GetBaseData(c, nodeName, "nodes")

		clientset, err := findClient(pattern, clusterContextName)
		if err != nil { return c.Render(200, "node-detail.html", NodeDetailPageData{PageBase: base}) }
		
		data := NodeDetailPageData{PageBase: base, ClusterName: clusterContextName, NodeName: nodeName, Capacity: make(map[string]string), Allocatable: make(map[string]string)}
		
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) 
		defer cancel()
		
		var wg sync.WaitGroup
		wg.Add(2) 

		// 1. Fetch Node Info & Pods
		go func() {
			defer wg.Done()
			node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
			if err == nil {
				data.KubeletVersion = node.Status.NodeInfo.KubeletVersion
				data.OS = node.Status.NodeInfo.OperatingSystem
				data.ContainerRuntime = node.Status.NodeInfo.ContainerRuntimeVersion
				data.ProviderID = node.Spec.ProviderID
				data.Age = formatAge(node.CreationTimestamp)
				
				// Addresses
				for _, addr := range node.Status.Addresses {
					data.Addresses = append(data.Addresses, NodeAddressInfo{Type: string(addr.Type), Address: addr.Address})
				}
				
				// Taints
				for _, t := range node.Spec.Taints {
					data.Taints = append(data.Taints, NodeTaintInfo{Key: t.Key, Value: t.Value, Effect: string(t.Effect)})
				}
				
				// Labels & Annotations
				data.Labels = node.Labels
				data.Annotations = node.Annotations
				
				// Conditions
				for _, c := range node.Status.Conditions {
					data.Conditions = append(data.Conditions, PodConditionInfo{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message, LastHeartbeat: formatAge(c.LastHeartbeatTime)})
				}
				
				// Capacity / Allocatable
				for k, v := range node.Status.Capacity { data.Capacity[string(k)] = v.String() }
				for k, v := range node.Status.Allocatable { data.Allocatable[string(k)] = v.String() }
				
				// Resources
				capCpu := node.Status.Capacity.Cpu(); capMem := node.Status.Capacity.Memory()
				allocCpu := node.Status.Allocatable.Cpu(); allocMem := node.Status.Allocatable.Memory()
				data.Resources.CapacityCpu = formatCpu(capCpu); data.Resources.CapacityMem = formatMemory(capMem)
				data.Resources.AllocatableCpu = formatCpu(allocCpu); data.Resources.AllocatableMem = formatMemory(allocMem)
				
				// Pods on Node
				fieldSelector := "spec.nodeName=" + nodeName
				pods, _ := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: fieldSelector})
				
				var usageCpu, usageMem resource.Quantity
				for _, p := range pods.Items {
					readyCount := 0
					for _, cs := range p.Status.ContainerStatuses { if cs.Ready { readyCount++ } }
					ready := fmt.Sprintf("%d/%d", readyCount, len(p.Spec.Containers))
					
					data.Pods = append(data.Pods, PodInfo{
						Namespace: p.Namespace, Name: p.Name, Status: string(p.Status.Phase), Ready: ready,
					})
					// Estimate usage from requests
					for _, c := range p.Spec.Containers {
						usageCpu.Add(*c.Resources.Requests.Cpu())
						usageMem.Add(*c.Resources.Requests.Memory())
					}
				}
				data.Resources.UsageCpu = formatCpu(&usageCpu)
				data.Resources.UsageMem = formatMemory(&usageMem)
				
				if capCpu.MilliValue() > 0 {
					data.Resources.AllocatableCpuPercent = (float64(allocCpu.MilliValue())/float64(capCpu.MilliValue()))*100
					data.Resources.UsageCpuPercent = (float64(usageCpu.MilliValue())/float64(capCpu.MilliValue()))*100
				}
				if capMem.Value() > 0 {
					data.Resources.AllocatableMemPercent = (float64(allocMem.Value())/float64(capMem.Value()))*100
					data.Resources.UsageMemPercent = (float64(usageMem.Value())/float64(capMem.Value()))*100
				}
			}
		}()
		
		// 2. Fetch Events
		go func() {
			defer wg.Done()
			eventFieldSelector := "involvedObject.kind=Node,involvedObject.name=" + nodeName
			eventList, _ := clientset.CoreV1().Events("").List(ctx, metav1.ListOptions{FieldSelector: eventFieldSelector})
			if eventList != nil {
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
		return c.Render(200, "node-detail.html", data)
	}
}