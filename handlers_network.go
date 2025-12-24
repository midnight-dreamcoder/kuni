package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/labstack/echo/v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- SERVICES ---

func handleGetServices(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		configsToProcess, err := getConfigsToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}

		base := PageBase{
			Title:                "Services",
			ActivePage:           "services",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clients, clientErrors := createClients(configsToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)

		type svcResult struct {
			ClusterName string
			Services    []ServiceInfo
		}

		fetch := func(client KubeClient) (svcResult, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			list, err := client.Clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{})
			if err != nil {
				return svcResult{}, err
			}
			var items []ServiceInfo
			for _, s := range list.Items {
				ext := ""
				if len(s.Status.LoadBalancer.Ingress) > 0 {
					ext = s.Status.LoadBalancer.Ingress[0].IP
				}
				items = append(items, ServiceInfo{
					Cluster:    client.ContextName,
					Namespace:  s.Namespace,
					Name:       s.Name,
					Type:       string(s.Spec.Type),
					ClusterIP:  s.Spec.ClusterIP,
					ExternalIP: ext,
					Age:        formatAge(s.CreationTimestamp),
				})
			}
			return svcResult{ClusterName: client.ContextName, Services: items}, nil
		}

		results, fetchErrors := ParallelFetch(clients, fetch)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		var allServices []ServiceInfo
		clusterStats := make(map[string]int)
		nsStats := make(map[string]int)

		for _, res := range results {
			for _, s := range res.Services {
				clusterStats[res.ClusterName]++
				nsStats[s.Namespace]++
				allServices = append(allServices, s)
			}
		}

		sort.Slice(allServices, func(i, j int) bool { return allServices[i].Name < allServices[j].Name })

		var cStats []ClusterStat
		for k, v := range clusterStats {
			cStats = append(cStats, ClusterStat{Name: k, Count: v})
		}
		sort.Slice(cStats, func(i, j int) bool { return cStats[i].Name < cStats[j].Name })

		var nStats []NamespaceStat
		for k, v := range nsStats {
			nStats = append(nStats, NamespaceStat{Name: k, Count: v})
		}
		sort.Slice(nStats, func(i, j int) bool { return nStats[i].Count > nStats[j].Count })
		if len(nStats) > 10 {
			nStats = nStats[:10]
		}

		data := ServicePageData{
			PageBase:       base,
			Services:       allServices,
			TotalServices:  len(allServices),
			ClusterStats:   cStats,
			NamespaceStats: nStats,
		}
		return c.Render(200, "services.html", data)
	}
}

func handleGetServiceDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		clusterContextName := c.QueryParam("cluster_name")
		namespace := c.QueryParam("namespace")
		serviceName := c.QueryParam("name")

		base := PageBase{
			Title:                serviceName,
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
			ServiceName:   serviceName,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		svc, err := clientset.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
		if err != nil {
			data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get service: %v", err))
		} else {
			data.Type = string(svc.Spec.Type)
			data.ClusterIP = svc.Spec.ClusterIP
			data.SessionAffinity = string(svc.Spec.SessionAffinity)
			data.Selector = svc.Spec.Selector
			data.Age = formatAge(svc.CreationTimestamp)
			for _, ingress := range svc.Status.LoadBalancer.Ingress {
				if ingress.IP != "" {
					data.ExternalIPs = append(data.ExternalIPs, ingress.IP)
				}
				if ingress.Hostname != "" {
					data.ExternalIPs = append(data.ExternalIPs, ingress.Hostname)
				}
			}
			for _, p := range svc.Spec.Ports {
				data.Ports = append(data.Ports, ServicePortInfo{
					Name:       p.Name,
					Port:       p.Port,
					TargetPort: p.TargetPort.String(),
					Protocol:   string(p.Protocol),
					NodePort:   p.NodePort,
				})
			}

			// Get Endpoints
			endpoints, err := clientset.CoreV1().Endpoints(namespace).Get(ctx, serviceName, metav1.GetOptions{})
			if err == nil {
				for _, subset := range endpoints.Subsets {
					for _, addr := range subset.Addresses {
						target := ""
						if addr.TargetRef != nil {
							target = fmt.Sprintf("%s/%s", addr.TargetRef.Kind, addr.TargetRef.Name)
						}
						node := ""
						if addr.NodeName != nil {
							node = *addr.NodeName
						}
						data.Endpoints = append(data.Endpoints, EndpointInfo{
							IP:        addr.IP,
							NodeName:  node,
							TargetRef: target,
							Ready:     true,
						})
					}
					for _, addr := range subset.NotReadyAddresses {
						target := ""
						if addr.TargetRef != nil {
							target = fmt.Sprintf("%s/%s", addr.TargetRef.Kind, addr.TargetRef.Name)
						}
						node := ""
						if addr.NodeName != nil {
							node = *addr.NodeName
						}
						data.Endpoints = append(data.Endpoints, EndpointInfo{
							IP:        addr.IP,
							NodeName:  node,
							TargetRef: target,
							Ready:     false,
						})
					}
				}
			}
		}

		// Events
		eventList, err := clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", serviceName, namespace),
		})
		if err == nil {
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

		return c.Render(200, "service-detail.html", data)
	}
}

// --- INGRESSES ---

func handleGetIngresses(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		configsToProcess, err := getConfigsToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}

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

		clients, clientErrors := createClients(configsToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)

		type ingResult struct {
			ClusterName string
			Items       []IngressInfo
		}

		fetch := func(client KubeClient) (ingResult, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			list, err := client.Clientset.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
			if err != nil {
				return ingResult{}, err
			}
			var items []IngressInfo
			for _, i := range list.Items {
				hosts := ""
				for _, r := range i.Spec.Rules {
					hosts += r.Host + " "
				}
				addr := ""
				if len(i.Status.LoadBalancer.Ingress) > 0 {
					addr = i.Status.LoadBalancer.Ingress[0].IP
				}
				items = append(items, IngressInfo{
					Cluster:   client.ContextName,
					Namespace: i.Namespace,
					Name:      i.Name,
					Hosts:     hosts,
					Address:   addr,
					Age:       formatAge(i.CreationTimestamp),
				})
			}
			return ingResult{ClusterName: client.ContextName, Items: items}, nil
		}

		results, fetchErrors := ParallelFetch(clients, fetch)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		var allIngresses []IngressInfo
		clusterStats := make(map[string]int)
		nsStats := make(map[string]int)

		for _, res := range results {
			for _, item := range res.Items {
				clusterStats[res.ClusterName]++
				nsStats[item.Namespace]++
				allIngresses = append(allIngresses, item)
			}
		}

		sort.Slice(allIngresses, func(i, j int) bool { return allIngresses[i].Name < allIngresses[j].Name })

		var cStats []ClusterStat
		for k, v := range clusterStats {
			cStats = append(cStats, ClusterStat{Name: k, Count: v})
		}
		sort.Slice(cStats, func(i, j int) bool { return cStats[i].Name < cStats[j].Name })

		var nStats []NamespaceStat
		for k, v := range nsStats {
			nStats = append(nStats, NamespaceStat{Name: k, Count: v})
		}
		sort.Slice(nStats, func(i, j int) bool { return nStats[i].Count > nStats[j].Count })
		if len(nStats) > 10 {
			nStats = nStats[:10]
		}

		data := IngressPageData{
			PageBase:       base,
			Ingresses:      allIngresses,
			TotalIngresses: len(allIngresses),
			ClusterStats:   cStats,
			NamespaceStats: nStats,
		}
		return c.Render(200, "ingresses.html", data)
	}
}

func handleGetIngressDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		clusterContextName := c.QueryParam("cluster_name")
		namespace := c.QueryParam("namespace")
		name := c.QueryParam("name")

		base := PageBase{
			Title:                name,
			ActivePage:           "ingresses",
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
			return c.Render(200, "ingress-detail.html", IngressDetailPageData{PageBase: base})
		}

		data := IngressDetailPageData{
			PageBase:      base,
			ClusterName:   clusterContextName,
			NamespaceName: namespace,
			IngressName:   name,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		ing, err := clientset.NetworkingV1().Ingresses(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			data.ErrorLogs = append(data.ErrorLogs, fmt.Sprintf("Failed to get ingress: %v", err))
		} else {
			if ing.Spec.IngressClassName != nil {
				data.ClassName = *ing.Spec.IngressClassName
			}
			if len(ing.Status.LoadBalancer.Ingress) > 0 {
				data.Address = ing.Status.LoadBalancer.Ingress[0].IP
			}
			data.Age = formatAge(ing.CreationTimestamp)
			data.Annotations = ing.Annotations

			for _, rule := range ing.Spec.Rules {
				for _, path := range rule.HTTP.Paths {
					svcName := "Unknown"
					svcPort := "Unknown"
					if path.Backend.Service != nil {
						svcName = path.Backend.Service.Name
						svcPort = fmt.Sprintf("%d", path.Backend.Service.Port.Number)
					}
					data.Rules = append(data.Rules, IngressRuleInfo{
						Host:        rule.Host,
						Path:        path.Path,
						PathType:    string(*path.PathType),
						ServiceName: svcName,
						ServicePort: svcPort,
					})
				}
			}
			for _, tls := range ing.Spec.TLS {
				data.TLS = append(data.TLS, fmt.Sprintf("Secret: %s, Hosts: %v", tls.SecretName, tls.Hosts))
			}
		}

		// Events
		eventList, err := clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", name, namespace),
		})
		if err == nil {
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

		return c.Render(200, "ingress-detail.html", data)
	}
}