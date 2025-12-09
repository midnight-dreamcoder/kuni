package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/api/core/v1"
)

// handleGetConfigMaps lists all configmaps from all clusters
func handleGetConfigMaps(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}
		
		// [UPDATED] Injected IsAdmin
		base := PageBase{
			Title:                "All ConfigMaps",
			ActivePage:           "configmaps",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		// --- SECURITY CHECK ---
		// If config.json says is_admin: false, redirect guests away.
		if !base.IsAdmin {
			return c.Redirect(302, "/overview?error=access_denied_admin_only")
		}
		// --- END CHECK ---

		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		
		var allConfigMaps []ConfigMapInfo
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
				
				cmList, err := client.Clientset.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{})
				if err != nil {
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list configmaps (%v)", client.ContextName, err))
					mutex.Unlock()
					return
				}

				mutex.Lock()
				for _, cm := range cmList.Items {
					clusterDistribution[client.ContextName]++
					namespaceDistribution[cm.Namespace]++
					
					allConfigMaps = append(allConfigMaps, ConfigMapInfo{
						Cluster:   client.ContextName,
						Namespace: cm.Namespace,
						Name:      cm.Name,
						DataKeys:  len(cm.Data),
						Age:       formatAge(cm.CreationTimestamp),
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

		data := ConfigMapPageData{
			PageBase:         base,
			ConfigMaps:       allConfigMaps,
			TotalConfigMaps:  len(allConfigMaps),
			ClusterStats:     clusterStats,
			NamespaceStats:   namespaceStats,
		}

		return c.Render(200, "configmaps.html", data)
	}
}

// handleGetConfigMapDetail fetches a single configmap and its events
func handleGetConfigMapDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		clusterContextName := c.QueryParam("cluster_name")
		namespace := c.QueryParam("namespace")
		cmName := c.QueryParam("name")
		if clusterContextName == "" || namespace == "" || cmName == "" {
			return c.String(400, "Missing required query parameters: cluster_name, namespace, name")
		}

		// [UPDATED] Injected IsAdmin
		base := PageBase{
			Title:                cmName,
			ActivePage:           "configmaps",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		// --- SECURITY CHECK ---
		if !base.IsAdmin {
			return c.Redirect(302, "/overview?error=access_denied_admin_only")
		}
		// --- END CHECK ---

		clientset, err := findClient(pattern, clusterContextName)
		if err != nil {
			base.ErrorLogs = append(base.ErrorLogs, err.Error())
			return c.Render(200, "configmap-detail.html", ConfigMapDetailPageData{PageBase: base})
		}
		data := ConfigMapDetailPageData{
			PageBase:      base,
			ClusterName:   clusterContextName,
			NamespaceName: namespace,
			ConfigMapName: cmName,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cm, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, cmName, metav1.GetOptions{})
		if err != nil {
			data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get configmap: %v", err))
		} else {
			data.Age = formatAge(cm.CreationTimestamp)
			data.Data = cm.Data
		}
		fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", cmName, namespace)
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
		return c.Render(200, "configmap-detail.html", data)
	}
}

// handleGetPVCs lists all PVCs
func handleGetPVCs(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}

		// [UPDATED] Injected IsAdmin
		base := PageBase{
			Title:                "PersistentVolumeClaims",
			ActivePage:           "pvcs",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		// --- SECURITY CHECK ---
		if !base.IsAdmin {
			return c.Redirect(302, "/overview?error=access_denied_admin_only")
		}
		// --- END CHECK ---

		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)

		var allPVCs []PVCInfo
		clusterDistribution := make(map[string]int)
		namespaceDistribution := make(map[string]int)
		statusDistribution := make(map[string]int)

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
				
				pvcList, err := client.Clientset.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
				if err != nil {
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list PVCs (%v)", client.ContextName, err))
					mutex.Unlock()
					return
				}

				mutex.Lock()
				for _, pvc := range pvcList.Items {
					clusterDistribution[client.ContextName]++
					namespaceDistribution[pvc.Namespace]++
					statusDistribution[string(pvc.Status.Phase)]++

					storage := "N/A"
					if cap, ok := pvc.Status.Capacity[v1.ResourceStorage]; ok {
						storage = formatMemory(&cap)
					}
					
					storageClass := "None"
					if pvc.Spec.StorageClassName != nil {
						storageClass = *pvc.Spec.StorageClassName
					}

					allPVCs = append(allPVCs, PVCInfo{
						Cluster:      client.ContextName,
						Namespace:    pvc.Namespace,
						Name:         pvc.Name,
						Status:       string(pvc.Status.Phase),
						VolumeName:   pvc.Spec.VolumeName,
						Capacity:     storage,
						StorageClass: storageClass,
						Age:          formatAge(pvc.CreationTimestamp),
					})
				}
				mutex.Unlock()
			}(client)
		}
		wg.Wait()

		// --- Stats ---
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

		var statusStats []PodStatusStat
		for s, c := range statusDistribution {
			statusStats = append(statusStats, PodStatusStat{Status: s, Count: c})
		}
		sort.Slice(statusStats, func(i, j int) bool { return statusStats[i].Count > statusStats[j].Count })

		data := PVCPageData{
			PageBase:       base,
			PVCs:           allPVCs,
			TotalPVCs:      len(allPVCs),
			ClusterStats:   clusterStats,
			NamespaceStats: namespaceStats,
			StatusStats:    statusStats,
		}

		return c.Render(200, "pvcs.html", data)
	}
}

// handleGetPVCDetail fetches a single PVC, finding which pods mount it
func handleGetPVCDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		clusterContextName := c.QueryParam("cluster_name")
		namespace := c.QueryParam("namespace")
		pvcName := c.QueryParam("name")
		if clusterContextName == "" || namespace == "" || pvcName == "" {
			return c.String(400, "Missing required query parameters")
		}

		// [UPDATED] Injected IsAdmin
		base := PageBase{
			Title:                pvcName,
			ActivePage:           "pvcs",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		// --- SECURITY CHECK ---
		if !base.IsAdmin {
			return c.Redirect(302, "/overview?error=access_denied_admin_only")
		}
		// --- END CHECK ---

		clientset, err := findClient(pattern, clusterContextName)
		if err != nil {
			base.ErrorLogs = append(base.ErrorLogs, err.Error())
			return c.Render(200, "pvc-detail.html", PVCDetailPageData{PageBase: base})
		}
		data := PVCDetailPageData{
			PageBase:      base,
			ClusterName:   clusterContextName,
			NamespaceName: namespace,
			PVCName:       pvcName,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var wg sync.WaitGroup
		var mutex sync.Mutex
		wg.Add(3)

		// 1. Get PVC Info
		go func() {
			defer wg.Done()
			pvc, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
			if err != nil {
				mutex.Lock(); data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get PVC: %v", err)); mutex.Unlock()
				return
			}
			data.Status = string(pvc.Status.Phase)
			data.Volume = pvc.Spec.VolumeName
			if cap, ok := pvc.Status.Capacity[v1.ResourceStorage]; ok {
				data.Capacity = formatMemory(&cap)
			} else {
				data.Capacity = "N/A"
			}
			if pvc.Spec.StorageClassName != nil {
				data.StorageClass = *pvc.Spec.StorageClassName
			} else {
				data.StorageClass = "None"
			}
			for _, mode := range pvc.Spec.AccessModes {
				data.AccessModes = append(data.AccessModes, string(mode))
			}
			data.Age = formatAge(pvc.CreationTimestamp)
		}()

		// 2. Find Pods mounting this PVC
		go func() {
			defer wg.Done()
			podList, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				mutex.Lock(); data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to scan pods: %v", err)); mutex.Unlock()
				return
			}
			for _, pod := range podList.Items {
				isMounted := false
				for _, vol := range pod.Spec.Volumes {
					if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName == pvcName {
						isMounted = true
						break
					}
				}
				if isMounted {
					readyCount := 0; for _, cs := range pod.Status.ContainerStatuses { if cs.Ready { readyCount++ } }
					readyStr := fmt.Sprintf("%d/%d", readyCount, len(pod.Spec.Containers))
					restartCount := 0; for _, cs := range pod.Status.ContainerStatuses { restartCount += int(cs.RestartCount) }
					
					podInfo := PodInfo{
						Cluster:   clusterContextName,
						Namespace: pod.Namespace,
						Name:      pod.Name,
						Ready:     readyStr,
						Status:    string(pod.Status.Phase),
						Reason:    getPodReason(pod),
						Restarts:  restartCount,
						Node:      pod.Spec.NodeName,
						PodIP:     pod.Status.PodIP,
						Age:       formatAge(pod.CreationTimestamp),
					}

					// Added Mutex
					mutex.Lock()
					data.MountedBy = append(data.MountedBy, podInfo)
					mutex.Unlock()
				}
			}
		}()

		// 3. Get Events
		go func() {
			defer wg.Done()
			fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", pvcName, namespace)
			eventList, err := clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: fieldSelector})
			if err != nil {
				mutex.Lock(); data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get events: %v", err)); mutex.Unlock()
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
		return c.Render(200, "pvc-detail.html", data)
	}
}

// handleGetServiceAccounts lists all service accounts from all clusters
func handleGetServiceAccounts(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}

		// [UPDATED] Injected IsAdmin
		base := PageBase{
			Title:                "Service Accounts",
			ActivePage:           "serviceaccounts",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		// --- SECURITY CHECK ---
		if !base.IsAdmin {
			return c.Redirect(302, "/overview?error=access_denied_admin_only")
		}
		// --- END CHECK ---


		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		
		var allSAs []ServiceAccountInfo
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
				
				saList, err := client.Clientset.CoreV1().ServiceAccounts("").List(ctx, metav1.ListOptions{})
				if err != nil {
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list service accounts (%v)", client.ContextName, err))
					mutex.Unlock()
					return
				}

				mutex.Lock()
				for _, sa := range saList.Items {
					clusterDistribution[client.ContextName]++
					namespaceDistribution[sa.Namespace]++
					
					allSAs = append(allSAs, ServiceAccountInfo{
						Cluster:   client.ContextName,
						Namespace: sa.Namespace,
						Name:      sa.Name,
						Secrets:   len(sa.Secrets),
						Age:       formatAge(sa.CreationTimestamp),
					})
				}
				mutex.Unlock()
			}(client)
		}
		wg.Wait()

		// Stats
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
			var otherSum int; for _, ns := range namespaceStats[topN:] { otherSum += ns.Count }; namespaceStats = namespaceStats[:topN]; namespaceStats = append(namespaceStats, NamespaceStat{Name: "Others", Count: otherSum})
		}

		data := ServiceAccountPageData{
			PageBase:             base,
			ServiceAccounts:      allSAs,
			TotalServiceAccounts: len(allSAs),
			ClusterStats:         clusterStats,
			NamespaceStats:       namespaceStats,
		}

		return c.Render(200, "serviceaccounts.html", data)
	}
}

// handleGetServiceAccountDetail fetches a SA and finds pods using it
func handleGetServiceAccountDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		clusterContextName := c.QueryParam("cluster_name")
		namespace := c.QueryParam("namespace")
		saName := c.QueryParam("name")
		if clusterContextName == "" || namespace == "" || saName == "" {
			return c.String(400, "Missing required query parameters")
		}

		// [UPDATED] Injected IsAdmin
		base := PageBase{
			Title:                saName,
			ActivePage:           "serviceaccounts",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		// --- SECURITY CHECK ---
		if !base.IsAdmin {
			return c.Redirect(302, "/overview?error=access_denied_admin_only")
		}
		// --- END CHECK ---

		clientset, err := findClient(pattern, clusterContextName)
		if err != nil {
			base.ErrorLogs = append(base.ErrorLogs, err.Error())
			return c.Render(200, "serviceaccount-detail.html", ServiceAccountDetailPageData{PageBase: base})
		}
		data := ServiceAccountDetailPageData{
			PageBase:           base,
			ClusterName:        clusterContextName,
			NamespaceName:      namespace,
			ServiceAccountName: saName,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		
		var wg sync.WaitGroup
		var mutex sync.Mutex
		wg.Add(3)

		// 1. Get SA
		go func() {
			defer wg.Done()
			sa, err := clientset.CoreV1().ServiceAccounts(namespace).Get(ctx, saName, metav1.GetOptions{})
			if err != nil {
				mutex.Lock(); data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get SA: %v", err)); mutex.Unlock()
				return
			}
			data.Age = formatAge(sa.CreationTimestamp)
			for _, s := range sa.Secrets {
				data.Secrets = append(data.Secrets, s.Name)
			}
			for _, s := range sa.ImagePullSecrets {
				data.ImagePullSecrets = append(data.ImagePullSecrets, s.Name)
			}
		}()

		// 2. Get Pods using this SA
		go func() {
			defer wg.Done()
			// Field selector is efficient for this
			podFieldSelector := fmt.Sprintf("spec.serviceAccountName=%s", saName)
			podList, podErr := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{FieldSelector: podFieldSelector})
			if podErr != nil {
				mutex.Lock(); data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to find pods: %v", podErr)); mutex.Unlock()
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
					Age:       formatAge(pod.CreationTimestamp),
				})
			}
		}()

		// 3. Get Events
		go func() {
			defer wg.Done()
			fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", saName, namespace)
			eventList, err := clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: fieldSelector})
			if err != nil {
				mutex.Lock(); data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get events: %v", err)); mutex.Unlock()
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
		return c.Render(200, "serviceaccount-detail.html", data)
	}
}

// handleGetSecrets lists all secrets
func handleGetSecrets(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil { return c.String(500, "Error finding kubeconfig files") }
		
		// [UPDATED] Injected IsAdmin
		base := PageBase{
			Title:                "Secrets",
			ActivePage:           "secrets",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		// --- SECURITY CHECK ---
		if !base.IsAdmin {
			return c.Redirect(302, "/overview?error=access_denied_admin_only")
		}
		// --- END CHECK ---

		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		
		var allSecrets []SecretInfo
		clusterDistribution := make(map[string]int)
		namespaceDistribution := make(map[string]int)
		typeDistribution := make(map[string]int)

		var wg sync.WaitGroup
		var mutex sync.Mutex
		
		for _, client := range clients {
			wg.Add(1)
			go func(client KubeClient) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				list, err := client.Clientset.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
				if err != nil {
					mutex.Lock(); base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: %v", client.ContextName, err)); mutex.Unlock()
					return
				}
				mutex.Lock()
				for _, s := range list.Items {
					clusterDistribution[client.ContextName]++
					namespaceDistribution[s.Namespace]++
					typeDistribution[string(s.Type)]++
					
					allSecrets = append(allSecrets, SecretInfo{
						Cluster: client.ContextName, Namespace: s.Namespace, Name: s.Name,
						Type: string(s.Type), KeyCount: len(s.Data), Age: formatAge(s.CreationTimestamp),
					})
				}
				mutex.Unlock()
			}(client)
		}
		wg.Wait()
		
		// Stats
		var clusterStats []ClusterStat; for n, c := range clusterDistribution { clusterStats = append(clusterStats, ClusterStat{Name: n, Count: c}) }; sort.Slice(clusterStats, func(i, j int) bool { return clusterStats[i].Name < clusterStats[j].Name })
		
		const topN = 10
		var namespaceStats []NamespaceStat; for n, c := range namespaceDistribution { namespaceStats = append(namespaceStats, NamespaceStat{Name: n, Count: c}) }; sort.Slice(namespaceStats, func(i, j int) bool { return namespaceStats[i].Count > namespaceStats[j].Count })
		if len(namespaceStats) > topN { var other int; for _, ns := range namespaceStats[topN:] { other += ns.Count }; namespaceStats = namespaceStats[:topN]; namespaceStats = append(namespaceStats, NamespaceStat{Name: "Others", Count: other}) }

		var typeStats []ReasonStat; for n, c := range typeDistribution { typeStats = append(typeStats, ReasonStat{Reason: n, Count: c}) }; sort.Slice(typeStats, func(i, j int) bool { return typeStats[i].Count > typeStats[j].Count })

		data := SecretPageData{
			PageBase: base, Secrets: allSecrets, TotalSecrets: len(allSecrets),
			ClusterStats: clusterStats, NamespaceStats: namespaceStats, TypeStats: typeStats,
		}
		return c.Render(200, "secrets.html", data)
	}
}

// handleGetSecretDetail fetches a single secret
func handleGetSecretDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		clusterContextName := c.QueryParam("cluster_name")
		namespace := c.QueryParam("namespace")
		name := c.QueryParam("name")
		if clusterContextName == "" || namespace == "" || name == "" { return c.String(400, "Missing params") }
		
		// [UPDATED] Injected IsAdmin
		base := PageBase{
			Title:                name,
			ActivePage:           "secrets",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		// --- SECURITY CHECK ---
		if !base.IsAdmin {
			return c.Redirect(302, "/overview?error=access_denied_admin_only")
		}
		// --- END CHECK ---
		
		clientset, err := findClient(pattern, clusterContextName)
		if err != nil { base.ErrorLogs = append(base.ErrorLogs, err.Error()); return c.Render(200, "secret-detail.html", SecretDetailPageData{PageBase: base}) }
		
		data := SecretDetailPageData{ PageBase: base, ClusterName: clusterContextName, NamespaceName: namespace, SecretName: name, Data: make(map[string]string) }
		
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		
		var wg sync.WaitGroup
		var mutex sync.Mutex
		wg.Add(2)
		
		// 1. Get Secret
		go func() {
			defer wg.Done()
			s, err := clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil { mutex.Lock(); data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get secret: %v", err)); mutex.Unlock(); return }
			
			data.Age = formatAge(s.CreationTimestamp)
			data.Type = string(s.Type)
			
			// We only send the keys and the raw bytes as string. 
			// The Template will handle the decoding display logic.
			for k, v := range s.Data {
				data.Data[k] = string(v) // Keep raw (which is base64 decoded bytes in Go map, but actually k8s API returns bytes)
			}
		}()
		
		// 2. Get Events
		go func() {
			defer wg.Done()
			fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", name, namespace)
			eventList, err := clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: fieldSelector})
			if err != nil { return }
			sort.Slice(eventList.Items, func(i, j int) bool { return eventList.Items[i].LastTimestamp.Time.After(eventList.Items[j].LastTimestamp.Time) })
			for _, e := range eventList.Items {
				data.Events = append(data.Events, EventInfo{
					Type: e.Type, Reason: e.Reason, Message: e.Message, Count: int(e.Count), LastSeen: formatAge(e.LastTimestamp),
				})
			}
		}()
		
		wg.Wait()
		return c.Render(200, "secret-detail.html", data)
	}
}