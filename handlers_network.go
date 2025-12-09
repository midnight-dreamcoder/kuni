package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// handleGetNamespaces lists all namespaces from all clusters
func handleGetNamespaces(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}
		
		// [UPDATED] Injected IsAdmin
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

		nsAggregator := make(map[string]map[string]bool)
		clusterDistribution := make(map[string]int)
		nsStatusMap := make(map[string]int)
		var totalInstances int
		nsStatusAgg := make(map[string]map[string]int)

		if len(filesToProcess) == 0 {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("No clusters selected or found matching pattern '%s'", pattern))
		}

		var wg sync.WaitGroup
		var mutex sync.Mutex

		for _, client := range clients {
			wg.Add(1)
			go func(client KubeClient) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				nsList, err := client.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
				cancel()
				if err != nil {
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list namespaces (%v)", client.ContextName, err))
					mutex.Unlock()
					return
				}

				mutex.Lock()
				totalInstances += len(nsList.Items)
				for _, ns := range nsList.Items {
					status := string(ns.Status.Phase)
					if _, ok := nsAggregator[ns.Name]; !ok {
						nsAggregator[ns.Name] = make(map[string]bool)
					}
					nsAggregator[ns.Name][client.ContextName] = true
					clusterDistribution[client.ContextName]++
					nsStatusMap[status]++
					if _, ok := nsStatusAgg[ns.Name]; !ok {
						nsStatusAgg[ns.Name] = make(map[string]int)
					}
					nsStatusAgg[ns.Name][status]++
				}
				mutex.Unlock()
			}(client)
		}
		wg.Wait()

		var finalView []AggregatedNamespaceView
		for nsName, clusterSet := range nsAggregator {
			var clusters []string
			for clusterName := range clusterSet {
				clusters = append(clusters, clusterName)
			}
			sort.Strings(clusters)
			finalView = append(finalView, AggregatedNamespaceView{
				Name:         nsName,
				Clusters:     clusters,
				StatusCounts: nsStatusAgg[nsName],
			})
		}
		sort.Slice(finalView, func(i, j int) bool {
			return finalView[i].Name < finalView[j].Name
		})
		var clusterStats []ClusterStat
		for clusterName, count := range clusterDistribution {
			clusterStats = append(clusterStats, ClusterStat{Name: clusterName, Count: count})
		}
		sort.Slice(clusterStats, func(i, j int) bool {
			return clusterStats[i].Name < clusterStats[j].Name
		})

		var nsStatusSlice []PodStatusStat
		for status, count := range nsStatusMap {
			nsStatusSlice = append(nsStatusSlice, PodStatusStat{Status: status, Count: count})
		}
		sort.Slice(nsStatusSlice, func(i, j int) bool {
			return nsStatusSlice[i].Count > nsStatusSlice[j].Count
		})

		data := NamespacePageData{
			PageBase:                base,
			Namespaces:              finalView,
			TotalUniqueNamespaces:   len(nsAggregator),
			ClusterStats:            clusterStats,
			NamespaceStatusStats:    nsStatusSlice,
			TotalNamespaceInstances: totalInstances,
		}
		return c.Render(200, "all-namespaces.html", data)
	}
}

// handleGetNamespaceDetail fetches a single namespace and its contents
func handleGetNamespaceDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		namespaceName := c.QueryParam("name")
		if namespaceName == "" {
			return c.String(400, "Missing required query parameter: name")
		}

		// [UPDATED] Injected IsAdmin
		base := PageBase{
			Title:                namespaceName,
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
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Error finding kubeconfig files: %v", err))
		}
		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		details := make(map[string]NamespaceDetailView)
		var clusterNames []string
		var wg sync.WaitGroup
		var mutex sync.Mutex
		if len(clients) == 0 {
			base.ErrorLogs = append(base.ErrorLogs, "No clusters selected to search.")
		}
		for _, client := range clients {
			wg.Add(1)
			go func(client KubeClient) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				
				_, err := client.Clientset.CoreV1().Namespaces().Get(ctx, namespaceName, metav1.GetOptions{})
				if err != nil {
					if errors.IsNotFound(err) {
						log.Printf("INFO: Namespace '%s' not found on cluster '%s'. Skipping.", namespaceName, client.ContextName)
					} else {
						log.Printf("ERROR: Failed to get namespace '%s' on cluster '%s': %v", namespaceName, client.ContextName, err)
						mutex.Lock()
						base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to get namespace (%v)", client.ContextName, err))
						mutex.Unlock()
					}
					return 
				}
				
				var detailView NamespaceDetailView
				var errLogs []string
				var subWg sync.WaitGroup
				subWg.Add(3)
				go func() {
					defer subWg.Done()
					depList, err := client.Clientset.AppsV1().Deployments(namespaceName).List(ctx, metav1.ListOptions{})
					if err != nil {
						errLogs = append(errLogs, fmt.Sprintf("Failed to list deployments: %v", err))
						return
					}
					detailView.TotalDeployments = len(depList.Items)
					for _, dep := range depList.Items {
						var images []string
						for _, c := range dep.Spec.Template.Spec.Containers {
							images = append(images, c.Image)
						}
						detailView.Deployments = append(detailView.Deployments, SimpleDeploymentInfo{
							Name:     dep.Name,
							Ready:    fmt.Sprintf("%d/%d", dep.Status.ReadyReplicas, dep.Status.Replicas),
							Images:   images,
							Strategy: string(dep.Spec.Strategy.Type),
							Age:      formatAge(dep.CreationTimestamp),
						})
					}
				}()
				go func() {
					defer subWg.Done()
					podList, err := client.Clientset.CoreV1().Pods(namespaceName).List(ctx, metav1.ListOptions{})
					if err != nil {
						errLogs = append(errLogs, fmt.Sprintf("Failed to list pods: %v", err))
						return
					}
					detailView.TotalPods = len(podList.Items)
					for _, pod := range podList.Items {
						readyCount := 0; for _, cs := range pod.Status.ContainerStatuses { if cs.Ready { readyCount++ } }
						readyStr := fmt.Sprintf("%d/%d", readyCount, len(pod.Spec.Containers))
						restartCount := 0; for _, cs := range pod.Status.ContainerStatuses { restartCount += int(cs.RestartCount) }
						nodeName := pod.Spec.NodeName; if nodeName == "" { nodeName = "N/A" }
						detailView.Pods = append(detailView.Pods, PodInfo{
							Cluster:   client.ContextName,
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
				}()
				go func() {
					defer subWg.Done()
					rsList, err := client.Clientset.AppsV1().ReplicaSets(namespaceName).List(ctx, metav1.ListOptions{})
					if err != nil {
						errLogs = append(errLogs, fmt.Sprintf("Failed to list replicasets: %v", err))
						return
					}
					detailView.TotalReplicaSets = len(rsList.Items)
					for _, rs := range rsList.Items {
						var desiredReplicas int32; if rs.Spec.Replicas != nil { desiredReplicas = *rs.Spec.Replicas }
						readyStr := fmt.Sprintf("%d/%d/%d", rs.Status.ReadyReplicas, desiredReplicas, rs.Status.Replicas)
						owner := "None"; if len(rs.OwnerReferences) > 0 { owner = rs.OwnerReferences[0].Name }
						detailView.ReplicaSets = append(detailView.ReplicaSets, ReplicaSetInfo{
							Name: rs.Name, Ready: readyStr, Owner: owner, Age: formatAge(rs.CreationTimestamp),
						})
					}
				}()
				subWg.Wait()
				
				mutex.Lock()
				details[client.ContextName] = detailView
				clusterNames = append(clusterNames, client.ContextName)
				for _, e := range errLogs {
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | %s", client.ContextName, e))
				}
				mutex.Unlock()
			}(client)
		}
		wg.Wait()
		sort.Strings(clusterNames)
		data := NamespaceDetailPageData{
			PageBase:      base,
			NamespaceName: namespaceName,
			ClusterNames:  clusterNames,
			Details:       details,
		}
		return c.Render(200, "namespace-detail.html", data)
	}
}

// handleGetServices lists all services from all clusters
func handleGetServices(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}

		// [UPDATED] Injected IsAdmin
		base := PageBase{
			Title:                "All Services",
			ActivePage:           "services",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		var allServices []ServiceInfo
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
				
				svcList, err := client.Clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{})
				if err != nil {
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list services (%v)", client.ContextName, err))
					mutex.Unlock()
					return
				}

				mutex.Lock()
				for _, svc := range svcList.Items {
					clusterDistribution[client.ContextName]++
					namespaceDistribution[svc.Namespace]++
					
					externalIP := "N/A"
					if len(svc.Status.LoadBalancer.Ingress) > 0 {
						if svc.Status.LoadBalancer.Ingress[0].IP != "" {
							externalIP = svc.Status.LoadBalancer.Ingress[0].IP
						} else if svc.Status.LoadBalancer.Ingress[0].Hostname != "" {
							externalIP = svc.Status.LoadBalancer.Ingress[0].Hostname
						}
					}

					allServices = append(allServices, ServiceInfo{
						Cluster:    client.ContextName,
						Namespace:  svc.Namespace,
						Name:       svc.Name,
						Type:       string(svc.Spec.Type),
						ClusterIP:  svc.Spec.ClusterIP,
						ExternalIP: externalIP,
						Age:        formatAge(svc.CreationTimestamp),
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

		data := ServicePageData{
			PageBase:       base,
			Services:       allServices,
			TotalServices:  len(allServices),
			ClusterStats:   clusterStats,
			NamespaceStats: namespaceStats,
		}

		return c.Render(200, "services.html", data)
	}
}

// handleGetServiceDetail fetches a single service, its endpoints, and events
func handleGetServiceDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		clusterContextName := c.QueryParam("cluster_name")
		namespace := c.QueryParam("namespace")
		svcName := c.QueryParam("name")

		if clusterContextName == "" || namespace == "" || svcName == "" {
			return c.String(400, "Missing required query parameters")
		}

		// [UPDATED] Injected IsAdmin
		base := PageBase{
			Title:                svcName,
			ActivePage:           "services",
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
			return c.Render(200, "service-detail.html", ServiceDetailPageData{PageBase: base})
		}

		data := ServiceDetailPageData{
			PageBase:      base,
			ClusterName:   clusterContextName,
			NamespaceName: namespace,
			ServiceName:   svcName,
		}
		
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var wg sync.WaitGroup
		var mutex sync.Mutex
		wg.Add(3) // Service, Endpoints, Events

		// 1. Get Service Info
		go func() {
			defer wg.Done()
			svc, err := clientset.CoreV1().Services(namespace).Get(ctx, svcName, metav1.GetOptions{})
			if err != nil {
				mutex.Lock()
				data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get service: %v", err))
				mutex.Unlock()
				return
			}
			data.Type = string(svc.Spec.Type)
			data.ClusterIP = svc.Spec.ClusterIP
			data.Age = formatAge(svc.CreationTimestamp)
			data.SessionAffinity = string(svc.Spec.SessionAffinity)
			data.Selector = svc.Spec.Selector
			data.ExternalIPs = svc.Spec.ExternalIPs
			
			// Add LB Ingress IPs
			for _, ingress := range svc.Status.LoadBalancer.Ingress {
				if ingress.IP != "" {
					data.ExternalIPs = append(data.ExternalIPs, ingress.IP)
				}
				if ingress.Hostname != "" {
					data.ExternalIPs = append(data.ExternalIPs, ingress.Hostname)
				}
			}
			
			// Parse Ports
			for _, p := range svc.Spec.Ports {
				data.Ports = append(data.Ports, ServicePortInfo{
					Name:       p.Name,
					Port:       p.Port,
					TargetPort: p.TargetPort.String(),
					Protocol:   string(p.Protocol),
					NodePort:   p.NodePort,
				})
			}
		}()

		// 2. Get Endpoints (The backends)
		go func() {
			defer wg.Done()
			ep, err := clientset.CoreV1().Endpoints(namespace).Get(ctx, svcName, metav1.GetOptions{})
			if err != nil {
				// It's okay if endpoints don't exist
				return 
			}
			
			for _, subset := range ep.Subsets {
				// Helper to safely get node name
				getNodeName := func(nodeNamePtr *string) string {
					if nodeNamePtr != nil {
						return *nodeNamePtr
					}
					return "N/A"
				}

				// Ready Addresses
				for _, addr := range subset.Addresses {
					target := ""
					if addr.TargetRef != nil {
						target = fmt.Sprintf("%s/%s", addr.TargetRef.Kind, addr.TargetRef.Name)
					}
					
					info := EndpointInfo{
						IP:        addr.IP,
						NodeName:  getNodeName(addr.NodeName),
						TargetRef: target,
						Ready:     true,
					}

					mutex.Lock()
					data.Endpoints = append(data.Endpoints, info)
					mutex.Unlock()
				}

				// Not Ready Addresses
				for _, addr := range subset.NotReadyAddresses {
					target := ""
					if addr.TargetRef != nil {
						target = fmt.Sprintf("%s/%s", addr.TargetRef.Kind, addr.TargetRef.Name)
					}
					
					info := EndpointInfo{
						IP:        addr.IP,
						NodeName:  getNodeName(addr.NodeName),
						TargetRef: target,
						Ready:     false,
					}

					mutex.Lock()
					data.Endpoints = append(data.Endpoints, info)
					mutex.Unlock()
				}
			}
		}()

		// 3. Get Events
		go func() {
			defer wg.Done()
			fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", svcName, namespace)
			eventList, err := clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: fieldSelector})
			if err != nil {
				mutex.Lock()
				data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get events: %v", err))
				mutex.Unlock()
				return
			}
			sort.Slice(eventList.Items, func(i, j int) bool {
				return eventList.Items[i].LastTimestamp.Time.After(eventList.Items[j].LastTimestamp.Time)
			})
			for _, e := range eventList.Items {
				mutex.Lock()
				data.Events = append(data.Events, EventInfo{
					Type: e.Type, Reason: e.Reason, Message: e.Message,
					Count: int(e.Count), LastSeen: formatAge(e.LastTimestamp),
				})
				mutex.Unlock()
			}
		}()

		wg.Wait()
		return c.Render(200, "service-detail.html", data)
	}
}

// handleGetIngresses lists all ingresses from all clusters
func handleGetIngresses(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil { return c.String(500, "Error finding kubeconfig files") }
		
		// [UPDATED] Injected IsAdmin
		base := PageBase{
			Title:                "Ingresses",
			ActivePage:           "ingresses",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		
		var allIngresses []IngressInfo
		clusterDistribution := make(map[string]int)
		namespaceDistribution := make(map[string]int)
		
		var wg sync.WaitGroup
		var mutex sync.Mutex
		
		for _, client := range clients {
			wg.Add(1)
			go func(client KubeClient) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				list, err := client.Clientset.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
				if err != nil {
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: %v", client.ContextName, err))
					mutex.Unlock()
					return
				}
				mutex.Lock()
				for _, ing := range list.Items {
					clusterDistribution[client.ContextName]++
					namespaceDistribution[ing.Namespace]++
					
					var hosts []string
					for _, r := range ing.Spec.Rules {
						if r.Host != "" {
							hosts = append(hosts, r.Host)
						}
					}
					
					address := ""
					if len(ing.Status.LoadBalancer.Ingress) > 0 {
						if ing.Status.LoadBalancer.Ingress[0].IP != "" {
							address = ing.Status.LoadBalancer.Ingress[0].IP
						} else {
							address = ing.Status.LoadBalancer.Ingress[0].Hostname
						}
					}
					
					allIngresses = append(allIngresses, IngressInfo{
						Cluster: client.ContextName, Namespace: ing.Namespace, Name: ing.Name,
						Hosts: strings.Join(hosts, ", "), Address: address, Age: formatAge(ing.CreationTimestamp),
					})
				}
				mutex.Unlock()
			}(client)
		}
		wg.Wait()
		
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
		if len(namespaceStats) > 10 {
			var other int
			for _, ns := range namespaceStats[10:] {
				other += ns.Count
			}
			namespaceStats = namespaceStats[:10]
			namespaceStats = append(namespaceStats, NamespaceStat{Name: "Others", Count: other})
		}

		data := IngressPageData{
			PageBase: base, Ingresses: allIngresses, TotalIngresses: len(allIngresses),
			ClusterStats: clusterStats, NamespaceStats: namespaceStats,
		}
		return c.Render(200, "ingresses.html", data)
	}
}

// handleGetIngressDetail fetches a single ingress
func handleGetIngressDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		clusterContextName := c.QueryParam("cluster_name")
		namespace := c.QueryParam("namespace")
		name := c.QueryParam("name")
		if clusterContextName == "" || namespace == "" || name == "" {
			return c.String(400, "Missing params")
		}
		
		// [UPDATED] Injected IsAdmin
		base := PageBase{
			Title:                name,
			ActivePage:           "ingresses",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clientset, err := findClient(pattern, clusterContextName)
		if err != nil {
			base.ErrorLogs = append(base.ErrorLogs, err.Error())
			return c.Render(200, "ingress-detail.html", IngressDetailPageData{PageBase: base})
		}
		
		data := IngressDetailPageData{ PageBase: base, ClusterName: clusterContextName, NamespaceName: namespace, IngressName: name }
		
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		
		var wg sync.WaitGroup
		var mutex sync.Mutex
		wg.Add(2)
		
		// 1. Get Ingress
		go func() {
			defer wg.Done()
			ing, err := clientset.NetworkingV1().Ingresses(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				mutex.Lock()
				data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get ingress: %v", err))
				mutex.Unlock()
				return
			}
			
			data.Age = formatAge(ing.CreationTimestamp)
			if ing.Spec.IngressClassName != nil {
				data.ClassName = *ing.Spec.IngressClassName
			}
			
			if len(ing.Status.LoadBalancer.Ingress) > 0 {
				if ing.Status.LoadBalancer.Ingress[0].IP != "" {
					data.Address = ing.Status.LoadBalancer.Ingress[0].IP
				} else {
					data.Address = ing.Status.LoadBalancer.Ingress[0].Hostname
				}
			}
			data.Annotations = ing.Annotations
			
			// Parse Rules
			for _, rule := range ing.Spec.Rules {
				host := rule.Host
				if host == "" {
					host = "*"
				}
				if rule.HTTP != nil {
					for _, path := range rule.HTTP.Paths {
						svcName := path.Backend.Service.Name
						svcPort := fmt.Sprintf("%d", path.Backend.Service.Port.Number)
						if path.Backend.Service.Port.Name != "" {
							svcPort = path.Backend.Service.Port.Name
						}
						pt := "Exact"
						if path.PathType != nil {
							pt = string(*path.PathType)
						}
						
						data.Rules = append(data.Rules, IngressRuleInfo{
							Host: host, Path: path.Path, PathType: pt, ServiceName: svcName, ServicePort: svcPort,
						})
					}
				}
			}
			// Parse TLS
			for _, t := range ing.Spec.TLS {
				hosts := strings.Join(t.Hosts, ", ")
				data.TLS = append(data.TLS, fmt.Sprintf("Secret: %s (Hosts: %s)", t.SecretName, hosts))
			}
		}()
		
		// 2. Get Events
		go func() {
			defer wg.Done()
			fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", name, namespace)
			eventList, err := clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: fieldSelector})
			if err != nil {
				return
			}
			sort.Slice(eventList.Items, func(i, j int) bool {
				return eventList.Items[i].LastTimestamp.Time.After(eventList.Items[j].LastTimestamp.Time)
			})
			for _, e := range eventList.Items {
				data.Events = append(data.Events, EventInfo{
					Type: e.Type, Reason: e.Reason, Message: e.Message, Count: int(e.Count), LastSeen: formatAge(e.LastTimestamp),
				})
			}
		}()
		wg.Wait()
		return c.Render(200, "ingress-detail.html", data)
	}
}