package main

import (
	"context"
	"fmt"
	"io"
	"log"
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
	"k8s.io/client-go/tools/clientcmd"
)

// handleGetClusters pings all clusters and shows their status
func handleGetClusters(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		
		base := PageBase{
			Title:                "Cluster Status",
			ActivePage:           "clusters",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		if errParam := c.QueryParam("error"); errParam != "" {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("❌ Error: %s", errParam))
		}
		if successParam := c.QueryParam("success"); successParam != "" {
			// [NEW] Use the new SuccessLogs slice
			base.SuccessLogs = append(base.SuccessLogs, fmt.Sprintf("✅ %s", successParam))
		}

		matchedFiles, err := filepath.Glob(pattern)
		if err != nil {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Error finding kubeconfig files: %v", err))
		}

		selectedQuery := c.QueryParams()["c"]
		selectedClustersMap := make(map[string]bool)
		for _, s := range selectedQuery {
			selectedClustersMap[s] = true
		}

		var clusters []ClusterInfo
		var wg sync.WaitGroup
		var mutex sync.Mutex

		if len(matchedFiles) == 0 {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("No config files found matching pattern: %s", pattern))
		}

		for _, configFile := range matchedFiles {
			wg.Add(1)
			go func(cfgFile string) {
				defer wg.Done()
				
				contextName := filepath.Base(cfgFile)
				loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
				loadingRules.ExplicitPath = cfgFile
				overrides := &clientcmd.ConfigOverrides{}
				kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
				
				rawConfig, rawErr := kubeConfig.RawConfig()
				if rawErr == nil && rawConfig.CurrentContext != "" {
					contextName = rawConfig.CurrentContext
				}

				info := ClusterInfo{
					Name:       contextName,
					ConfigName: filepath.Base(cfgFile),
					Status:     "Unknown",
					Latency:    "N/A",
					Version:    "N/A",
					Provider:   "Unknown",
				}

				config, err := kubeConfig.ClientConfig()
				if err != nil {
					info.Status = fmt.Sprintf("Invalid Config: %v", err)
					mutex.Lock()
					clusters = append(clusters, info)
					mutex.Unlock()
					return
				}
				
				info.ApiServer = config.Host

				clientset, err := kubernetes.NewForConfig(config)
				if err != nil {
					info.Status = fmt.Sprintf("Clientset Error: %v", err)
					mutex.Lock()
					clusters = append(clusters, info)
					mutex.Unlock()
					return
				}

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				
				start := time.Now()
				versionInfo, err := clientset.Discovery().ServerVersion()
				latency := time.Since(start)

				if err != nil {
					info.Status = "Offline"
					info.Latency = fmt.Sprintf("%d ms", latency.Milliseconds())
				} else {
					info.Status = "Online"
					info.Latency = fmt.Sprintf("%d ms", latency.Milliseconds())
					
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

					nodes, nodeErr := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
					if nodeErr == nil {
						info.NodeCount = len(nodes.Items)
						if info.NodeCount > 0 {
							pID := nodes.Items[0].Spec.ProviderID
							if strings.HasPrefix(pID, "aws") {
								info.Provider = "AWS"
							} else if strings.HasPrefix(pID, "gce") {
								info.Provider = "GCP"
							} else if strings.HasPrefix(pID, "azure") {
								info.Provider = "Azure"
							}
						}
					}
				}
				
				mutex.Lock()
				clusters = append(clusters, info)
				mutex.Unlock()
			}(configFile)
		}
		wg.Wait()

		sort.Slice(clusters, func(i, j int) bool { return clusters[i].Name < clusters[j].Name })

		data := ClusterPageData{
			PageBase:         base,
			Clusters:         clusters,
			SelectedClusters: selectedClustersMap,
		}
		return c.Render(200, "clusters.html", data)
	}
}

// handleUploadConfig saves a new kubeconfig file to the .kube directory
func handleUploadConfig(kubeDir string) echo.HandlerFunc {
	return func(c echo.Context) error {
		// [SECURITY] Block upload in Guest Mode
		if !CurrentConfig.IsAdmin {
			log.Println("⛔ Blocked config upload attempt (Guest Mode)")
			return c.Redirect(302, "/clusters?error=upload_not_allowed_in_guest_mode")
		}

		file, err := c.FormFile("kubeconfig")
		if err != nil {
			log.Printf("ERROR: Failed to get file from form: %v", err)
			return c.Redirect(302, "/clusters?error=upload_failed")
		}

		src, err := file.Open()
		if err != nil {
			log.Printf("ERROR: Failed to open uploaded file: %v", err)
			return c.Redirect(302, "/clusters?error=upload_read_failed")
		}
		defer src.Close()

		filename := fmt.Sprintf("config-ops-upload-%d.yaml", time.Now().Unix())
		dstPath := filepath.Join(kubeDir, filename)

		dst, err := os.Create(dstPath)
		if err != nil {
			log.Printf("ERROR: Failed to create destination file %s: %v", dstPath, err)
			return c.Redirect(302, "/clusters?error=upload_save_failed")
		}
		defer dst.Close()

		if _, err = io.Copy(dst, src); err != nil {
			log.Printf("ERROR: Failed to copy file data: %v", err)
			return c.Redirect(302, "/clusters?error=upload_copy_failed")
		}

		log.Printf("SUCCESS: New config file uploaded and saved to %s", dstPath)
		return c.Redirect(302, "/clusters")
	}
}

// handleGetClusterDetail fetches all details for a single cluster
func handleGetClusterDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		clusterContextName := c.QueryParam("cluster_name")
		if clusterContextName == "" {
			return c.String(400, "Missing required query parameter: cluster_name")
		}

		base := PageBase{
			Title:                clusterContextName,
			ActivePage:           "clusters",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

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

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
				log.Printf("No metrics client: %v", metricsErr)
				return
			}
			metricsList, err := metricsClient.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
			if err != nil {
				mutex.Lock()
				data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to list node metrics: %v", err))
				mutex.Unlock()
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

// handleGetClusterOverview provides a high-level dashboard of all cluster resources.
func handleGetClusterOverview(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}

		base := PageBase{
			Title:                "Cluster Overview",
			ActivePage:           "overview",
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

		var nodeStat WorkloadStat
		pvStatusMap := make(map[string]int)
		var totalPVs int
		
		resourceMap := make(map[string]ResourceSummary)
		var clusterNames []string

		if len(filesToProcess) == 0 {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("No clusters selected or found matching pattern '%s'", pattern))
		}

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		for _, client := range clients {
			wg.Add(1)
			go func(client KubeClient) {
				defer wg.Done()
				var clusterCapCpu, clusterAllocCpu, clusterUsageCpu resource.Quantity
				var clusterCapMem, clusterAllocMem, clusterUsageMem resource.Quantity
				var subWg sync.WaitGroup
				subWg.Add(3)

				go func() {
					defer subWg.Done()
					nodeList, err := client.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
					if err != nil {
						mutex.Lock()
						base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Nodes Error: %v", client.ContextName, err))
						mutex.Unlock()
						return
					}
					mutex.Lock()
					for _, node := range nodeList.Items {
						nodeStat.Total++
						status, _ := getNodeStatus(node)
						if status == "Ready" {
							nodeStat.Ready++
						} else {
							nodeStat.NotReady++
						}
						clusterCapCpu.Add(*node.Status.Capacity.Cpu())
						clusterAllocCpu.Add(*node.Status.Allocatable.Cpu())
						clusterCapMem.Add(*node.Status.Capacity.Memory())
						clusterAllocMem.Add(*node.Status.Allocatable.Memory())
					}
					mutex.Unlock()
				}()

				go func() {
					defer subWg.Done()
					pvList, err := client.Clientset.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
					if err != nil { return }
					mutex.Lock()
					totalPVs += len(pvList.Items)
					for _, pv := range pvList.Items {
						pvStatusMap[string(pv.Status.Phase)]++
					}
					mutex.Unlock()
				}()

				go func() {
					defer subWg.Done()
					metricsClient, err := findMetricsClient(pattern, client.ContextName)
					if err != nil { return }
					metricsList, err := metricsClient.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
					if err != nil { return }
					mutex.Lock()
					for _, metrics := range metricsList.Items {
						clusterUsageCpu.Add(*metrics.Usage.Cpu())
						clusterUsageMem.Add(*metrics.Usage.Memory())
					}
					mutex.Unlock()
				}()

				subWg.Wait()

				var summary ResourceSummary
				summary.CapacityCpu = formatCpu(&clusterCapCpu)
				summary.AllocatableCpu = formatCpu(&clusterAllocCpu)
				summary.UsageCpu = formatCpu(&clusterUsageCpu)
				summary.CapacityMem = formatMemory(&clusterCapMem)
				summary.AllocatableMem = formatMemory(&clusterAllocMem)
				summary.UsageMem = formatMemory(&clusterUsageMem)

				if clusterCapCpu.MilliValue() > 0 {
					summary.AllocatableCpuPercent = (float64(clusterAllocCpu.MilliValue()) / float64(clusterCapCpu.MilliValue())) * 100.0
					summary.UsageCpuPercent = (float64(clusterUsageCpu.MilliValue()) / float64(clusterCapCpu.MilliValue())) * 100.0
				}
				if clusterCapMem.Value() > 0 {
					summary.AllocatableMemPercent = (float64(clusterAllocMem.Value()) / float64(clusterCapMem.Value())) * 100.0
					summary.UsageMemPercent = (float64(clusterUsageMem.Value()) / float64(clusterCapMem.Value())) * 100.0
				}
				
				mutex.Lock()
				resourceMap[client.ContextName] = summary
				clusterNames = append(clusterNames, client.ContextName)
				mutex.Unlock()
			}(client)
		}
		wg.Wait()

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
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}
		
		base := PageBase{
			Title:                "All Nodes",
			ActivePage:           "nodes",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		var allNodes []NodeInfo
		clusterDistribution := make(map[string]int)
		statusMap := make(map[string]int)
		schedMap := make(map[string]int)

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
				nodeList, err := client.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
				cancel()
				if err != nil {
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list nodes (%v)", client.ContextName, err))
					mutex.Unlock()
					return
				}

				mutex.Lock()
				for _, node := range nodeList.Items {
					clusterDistribution[client.ContextName]++
					status, reason := getNodeStatus(node)
					schedulable := "Enabled"
					if node.Spec.Unschedulable {
						schedulable = "Disabled"
					}
					statusMap[status]++
					schedMap[schedulable]++
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
					allNodes = append(allNodes, NodeInfo{
						Cluster:     client.ContextName,
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
				}
				mutex.Unlock()
			}(client)
		}
		wg.Wait()

		var clusterStats []ClusterStat
		for clusterName, count := range clusterDistribution {
			clusterStats = append(clusterStats, ClusterStat{Name: clusterName, Count: count})
		}
		sort.Slice(clusterStats, func(i, j int) bool { return clusterStats[i].Name < clusterStats[j].Name })

		var statusSlice []PodStatusStat
		for status, count := range statusMap {
			statusSlice = append(statusSlice, PodStatusStat{Status: status, Count: count})
		}
		sort.Slice(statusSlice, func(i, j int) bool { return statusSlice[i].Count > statusSlice[j].Count })

		var schedSlice []PodStatusStat
		for sched, count := range schedMap {
			schedSlice = append(schedSlice, PodStatusStat{Status: sched, Count: count})
		}
		sort.Slice(schedSlice, func(i, j int) bool { return schedSlice[i].Count > schedSlice[j].Count })

		data := NodePageData{
			PageBase:     base,
			Nodes:        allNodes,
			TotalNodes:   len(allNodes),
			ClusterStats: clusterStats,
			StatusStats:  statusSlice,
			SchedStats:   schedSlice,
		}
		return c.Render(200, "nodes.html", data)
	}
}

// handleGetNodeDetail fetches a single node and its pods/events
func handleGetNodeDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		clusterContextName := c.QueryParam("cluster_name")
		nodeName := c.QueryParam("name")
		if clusterContextName == "" || nodeName == "" {
			return c.String(400, "Missing required query parameters: cluster_name, name")
		}

		base := PageBase{
			Title:                nodeName,
			ActivePage:           "nodes",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clientset, err := findClient(pattern, clusterContextName)
		if err != nil {
			base.ErrorLogs = append(base.ErrorLogs, err.Error())
			return c.Render(200, "node-detail.html", NodeDetailPageData{PageBase: base})
		}
		data := NodeDetailPageData{
			PageBase:    base,
			ClusterName: clusterContextName,
			NodeName:    nodeName,
			Capacity:    make(map[string]string),
			Allocatable: make(map[string]string),
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var wg sync.WaitGroup
		var mutex sync.Mutex
		wg.Add(3)
		var totalCapCpu, totalAllocCpu resource.Quantity
		var totalCapMem, totalAllocMem resource.Quantity
		go func() {
			defer wg.Done()
			node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
			if err != nil {
				mutex.Lock()
				data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get node: %v", err))
				mutex.Unlock()
				return
			}
			data.KubeletVersion = node.Status.NodeInfo.KubeletVersion
			data.OS = node.Status.NodeInfo.OperatingSystem
			data.ContainerRuntime = node.Status.NodeInfo.ContainerRuntimeVersion
			data.ProviderID = node.Spec.ProviderID
			data.Age = formatAge(node.CreationTimestamp)
			data.Labels = node.Labels
			data.Annotations = node.Annotations
			for _, addr := range node.Status.Addresses {
				data.Addresses = append(data.Addresses, NodeAddressInfo{
					Type:    string(addr.Type),
					Address: addr.Address,
				})
			}
			for _, taint := range node.Spec.Taints {
				data.Taints = append(data.Taints, NodeTaintInfo{
					Key:    taint.Key,
					Value:  taint.Value,
					Effect: string(taint.Effect),
				})
			}
			for _, cond := range node.Status.Conditions {
				data.Conditions = append(data.Conditions, PodConditionInfo{
					Type:          string(cond.Type),
					Status:        string(cond.Status),
					Reason:        cond.Reason,
					Message:       cond.Message,
					LastHeartbeat: formatAge(cond.LastHeartbeatTime),
				})
			}
			for key, val := range node.Status.Capacity {
				data.Capacity[string(key)] = val.String()
			}
			for key, val := range node.Status.Allocatable {
				data.Allocatable[string(key)] = val.String()
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
			data.Role = role
			mutex.Lock()
			totalCapCpu.Add(*node.Status.Capacity.Cpu())
			totalAllocCpu.Add(*node.Status.Allocatable.Cpu())
			totalCapMem.Add(*node.Status.Capacity.Memory())
			totalAllocMem.Add(*node.Status.Allocatable.Memory())
			mutex.Unlock()
		}()
		go func() {
			defer wg.Done()
			podFieldSelector := fmt.Sprintf("spec.nodeName=%s", nodeName)
			podList, podErr := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: podFieldSelector})
			if podErr != nil {
				mutex.Lock()
				data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to list pods: %v", podErr))
				mutex.Unlock()
				return
			}
			for _, pod := range podList.Items {
				readyCount := 0; for _, cs := range pod.Status.ContainerStatuses { if cs.Ready { readyCount++ } }
				readyStr := fmt.Sprintf("%d/%d", readyCount, len(pod.Spec.Containers))
				restartCount := 0; for _, cs := range pod.Status.ContainerStatuses { restartCount += int(cs.RestartCount) }
				data.Pods = append(data.Pods, PodInfo{
					Cluster:   clusterContextName,
					Namespace: pod.Namespace,
					Name:      pod.Name,
					Ready:     readyStr,
					Status:    string(pod.Status.Phase),
					Reason:    getPodReason(pod),
					Restarts:  restartCount,
					Node:      pod.Spec.NodeName,
					PodIP:     pod.Status.PodIP,
					QoS:       string(pod.Status.QOSClass),
					Age:       formatAge(pod.CreationTimestamp),
				})
			}
		}()
		go func() {
			defer wg.Done()
			eventFieldSelector := fmt.Sprintf("involvedObject.kind=Node,involvedObject.name=%s", nodeName)
			eventList, eventErr := clientset.CoreV1().Events("").List(ctx, metav1.ListOptions{FieldSelector: eventFieldSelector, Limit: 50})
			if eventErr != nil {
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
		wg.Wait()
		
		data.Resources.CapacityCpu = formatCpu(&totalCapCpu)
		data.Resources.AllocatableCpu = formatCpu(&totalAllocCpu)
		
		data.Resources.CapacityMem = formatMemory(&totalCapMem)
		data.Resources.AllocatableMem = formatMemory(&totalAllocMem)
		
		if totalCapCpu.MilliValue() > 0 {
			data.Resources.AllocatableCpuPercent = (float64(totalAllocCpu.MilliValue()) / float64(totalCapCpu.MilliValue())) * 100.0
		}
		if totalCapMem.Value() > 0 {
			data.Resources.AllocatableMemPercent = (float64(totalAllocMem.Value()) / float64(totalCapMem.Value())) * 100.0
		}

		return c.Render(200, "node-detail.html", data)
	}
}