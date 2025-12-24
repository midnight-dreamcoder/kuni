package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/labstack/echo/v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- REPLICA SETS ---

// handleGetReplicaSets lists all ReplicaSets from selected clusters
func handleGetReplicaSets(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		configsToProcess, err := getConfigsToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding configs")
		}
		
		base := PageBase{
			Title:                "ReplicaSets",
			ActivePage:           "replicasets",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clients, clientErrors := createClients(configsToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)

		type rsResult struct {
			ClusterName string
			Items       []ReplicaSetInfo
			Stat        ClusterStat
			NSCount     map[string]int
		}

		fetchRS := func(client KubeClient) (rsResult, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			list, err := client.Clientset.AppsV1().ReplicaSets("").List(ctx, metav1.ListOptions{})
			if err != nil {
				return rsResult{}, err
			}
			var items []ReplicaSetInfo
			nsCount := make(map[string]int)
			for _, rs := range list.Items {
				owner := "-"
				if len(rs.OwnerReferences) > 0 {
					owner = rs.OwnerReferences[0].Kind + "/" + rs.OwnerReferences[0].Name
				}
				var replicas int32 = 1
				if rs.Spec.Replicas != nil {
					replicas = *rs.Spec.Replicas
				}
				readyStr := fmt.Sprintf("%d/%d", rs.Status.ReadyReplicas, replicas)
				items = append(items, ReplicaSetInfo{
					Cluster:   client.ContextName,
					Namespace: rs.Namespace,
					Name:      rs.Name,
					Ready:     readyStr,
					Owner:     owner,
					Age:       formatAge(rs.CreationTimestamp),
				})
				nsCount[rs.Namespace]++
			}
			return rsResult{
				ClusterName: client.ContextName,
				Items:       items,
				Stat:        ClusterStat{Name: client.ContextName, Count: len(items)},
				NSCount:     nsCount,
			}, nil
		}
		
		results, fetchErrors := ParallelFetch(clients, fetchRS)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		var allRS []ReplicaSetInfo
		var cStats []ClusterStat
		gNS := make(map[string]int)

		for _, res := range results {
			allRS = append(allRS, res.Items...)
			cStats = append(cStats, res.Stat)
			for n, c := range res.NSCount {
				gNS[n] += c
			}
		}
		
		var nsStats []NamespaceStat
		for n, c := range gNS {
			nsStats = append(nsStats, NamespaceStat{Name: n, Count: c})
		}
		sort.Slice(nsStats, func(i, j int) bool { return nsStats[i].Count > nsStats[j].Count })
		
		return c.Render(200, "replicasets.html", ReplicaSetPageData{
			PageBase:       base,
			ReplicaSets:    allRS,
			TotalReplicaSets: len(allRS),
			ClusterStats:   cStats,
			NamespaceStats: nsStats,
		})
	}
}

// handleGetReplicaSetDetail fetches details for a single ReplicaSet
func handleGetReplicaSetDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		cluster := c.QueryParam("cluster_name")
		ns := c.QueryParam("namespace")
		name := c.QueryParam("name")

		base := PageBase{
			Title:                name,
			ActivePage:           "replicasets",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clientset, err := findClient(pattern, cluster)
		if err != nil {
			return c.String(404, "Cluster not found")
		}

		data := ReplicaSetDetailPageData{
			PageBase:       base,
			ClusterName:    cluster,
			NamespaceName:  ns,
			ReplicaSetName: name,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		rs, err := clientset.AppsV1().ReplicaSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			var replicas int32 = 1
			if rs.Spec.Replicas != nil {
				replicas = *rs.Spec.Replicas
			}
			data.Status = fmt.Sprintf("%d/%d Ready", rs.Status.ReadyReplicas, replicas)
			data.Selector = metav1.FormatLabelSelector(rs.Spec.Selector)
			data.Age = formatAge(rs.CreationTimestamp)
			if len(rs.OwnerReferences) > 0 {
				data.OwnerName = rs.OwnerReferences[0].Name
				data.OwnerKind = rs.OwnerReferences[0].Kind
			} else {
				data.OwnerName = "None"
			}

			// Fetch Pods
			podList, _ := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: data.Selector})
			for _, p := range podList.Items {
				data.Pods = append(data.Pods, PodInfo{
					Name: p.Name, Status: string(p.Status.Phase), Node: p.Spec.NodeName, Age: formatAge(p.CreationTimestamp),
				})
			}
		}

		// Fetch Events
		events, _ := clientset.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + name})
		for _, e := range events.Items {
			data.Events = append(data.Events, EventInfo{
				Type: e.Type, Reason: e.Reason, Message: e.Message, Count: int(e.Count), LastSeen: formatAge(e.LastTimestamp),
			})
		}

		return c.Render(200, "replicaset-detail.html", data)
	}
}

// --- DAEMON SETS ---

// handleGetDaemonSets lists all DaemonSets
func handleGetDaemonSets(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		configsToProcess, err := getConfigsToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding configs")
		}
		
		base := PageBase{
			Title:                "DaemonSets",
			ActivePage:           "daemonsets",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clients, clientErrors := createClients(configsToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		
		type dsResult struct {
			ClusterName string
			Items       []DaemonSetInfo
			Stat        ClusterStat
		}

		fetchDS := func(client KubeClient) (dsResult, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			list, err := client.Clientset.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{})
			if err != nil {
				return dsResult{}, err
			}
			var items []DaemonSetInfo
			for _, ds := range list.Items {
				readyStr := fmt.Sprintf("%d/%d", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
				items = append(items, DaemonSetInfo{
					Cluster:   client.ContextName,
					Namespace: ds.Namespace,
					Name:      ds.Name,
					Ready:     readyStr,
					Age:       formatAge(ds.CreationTimestamp),
				})
			}
			return dsResult{
				ClusterName: client.ContextName,
				Items:       items,
				Stat:        ClusterStat{Name: client.ContextName, Count: len(items)},
			}, nil
		}

		results, fetchErrors := ParallelFetch(clients, fetchDS)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		var allDS []DaemonSetInfo
		var cStats []ClusterStat
		for _, res := range results {
			allDS = append(allDS, res.Items...)
			cStats = append(cStats, res.Stat)
		}
		
		return c.Render(200, "daemonsets.html", DaemonSetPageData{
			PageBase:       base,
			DaemonSets:     allDS,
			TotalDaemonSets: len(allDS),
			ClusterStats:   cStats,
		})
	}
}

// handleGetDaemonSetDetail fetches details for a single DaemonSet
func handleGetDaemonSetDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		cluster := c.QueryParam("cluster_name")
		ns := c.QueryParam("namespace")
		name := c.QueryParam("name")

		base := PageBase{
			Title:                name,
			ActivePage:           "daemonsets",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clientset, err := findClient(pattern, cluster)
		if err != nil {
			return c.String(404, "Cluster not found")
		}

		data := DaemonSetDetailPageData{
			PageBase:      base,
			ClusterName:   cluster,
			NamespaceName: ns,
			DaemonSetName: name,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		ds, err := clientset.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			data.Overview.Status = fmt.Sprintf("%d Desired, %d Ready", ds.Status.DesiredNumberScheduled, ds.Status.NumberReady)
			data.Overview.Selector = metav1.FormatLabelSelector(ds.Spec.Selector)
			data.Overview.Age = formatAge(ds.CreationTimestamp)
			
			// Fetch Pods
			podList, _ := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: data.Overview.Selector})
			for _, p := range podList.Items {
				data.Pods = append(data.Pods, PodInfo{
					Name: p.Name, Status: string(p.Status.Phase), Node: p.Spec.NodeName, Age: formatAge(p.CreationTimestamp),
				})
			}
		}

		events, _ := clientset.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + name})
		for _, e := range events.Items {
			data.Events = append(data.Events, EventInfo{
				Type: e.Type, Reason: e.Reason, Message: e.Message, Count: int(e.Count), LastSeen: formatAge(e.LastTimestamp),
			})
		}

		return c.Render(200, "daemonset-detail.html", data)
	}
}

// --- STATEFUL SETS ---

// handleGetStatefulSets lists all StatefulSets
func handleGetStatefulSets(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		configsToProcess, err := getConfigsToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding configs")
		}
		
		base := PageBase{
			Title:                "StatefulSets",
			ActivePage:           "statefulsets",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clients, clientErrors := createClients(configsToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		
		type ssResult struct {
			ClusterName string
			Items       []StatefulSetInfo
			Stat        ClusterStat
		}

		fetchSS := func(client KubeClient) (ssResult, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			list, err := client.Clientset.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{})
			if err != nil {
				return ssResult{}, err
			}
			var items []StatefulSetInfo
			for _, ss := range list.Items {
				var replicas int32 = 1
				if ss.Spec.Replicas != nil {
					replicas = *ss.Spec.Replicas
				}
				readyStr := fmt.Sprintf("%d/%d", ss.Status.ReadyReplicas, replicas)
				items = append(items, StatefulSetInfo{
					Cluster:   client.ContextName,
					Namespace: ss.Namespace,
					Name:      ss.Name,
					Ready:     readyStr,
					Age:       formatAge(ss.CreationTimestamp),
				})
			}
			return ssResult{
				ClusterName: client.ContextName,
				Items:       items,
				Stat:        ClusterStat{Name: client.ContextName, Count: len(items)},
			}, nil
		}

		results, fetchErrors := ParallelFetch(clients, fetchSS)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		var allSS []StatefulSetInfo
		var cStats []ClusterStat
		for _, res := range results {
			allSS = append(allSS, res.Items...)
			cStats = append(cStats, res.Stat)
		}
		
		return c.Render(200, "statefulsets.html", StatefulSetPageData{
			PageBase:        base,
			StatefulSets:    allSS,
			TotalStatefulSets: len(allSS),
			ClusterStats:    cStats,
		})
	}
}

// handleGetStatefulSetDetail fetches details for a single StatefulSet
func handleGetStatefulSetDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		cluster := c.QueryParam("cluster_name")
		ns := c.QueryParam("namespace")
		name := c.QueryParam("name")

		base := PageBase{
			Title:                name,
			ActivePage:           "statefulsets",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clientset, err := findClient(pattern, cluster)
		if err != nil {
			return c.String(404, "Cluster not found")
		}

		data := StatefulSetDetailPageData{
			PageBase:        base,
			ClusterName:     cluster,
			NamespaceName:   ns,
			StatefulSetName: name,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		ss, err := clientset.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			var replicas int32 = 1
			if ss.Spec.Replicas != nil {
				replicas = *ss.Spec.Replicas
			}
			data.Overview.Status = fmt.Sprintf("%d/%d Ready", ss.Status.ReadyReplicas, replicas)
			data.Overview.Selector = metav1.FormatLabelSelector(ss.Spec.Selector)
			data.Overview.ServiceName = ss.Spec.ServiceName
			data.Overview.Age = formatAge(ss.CreationTimestamp)

			podList, _ := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: data.Overview.Selector})
			for _, p := range podList.Items {
				data.Pods = append(data.Pods, PodInfo{
					Name: p.Name, Status: string(p.Status.Phase), Node: p.Spec.NodeName, Age: formatAge(p.CreationTimestamp),
				})
			}
		}

		events, _ := clientset.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + name})
		for _, e := range events.Items {
			data.Events = append(data.Events, EventInfo{
				Type: e.Type, Reason: e.Reason, Message: e.Message, Count: int(e.Count), LastSeen: formatAge(e.LastTimestamp),
			})
		}

		return c.Render(200, "statefulset-detail.html", data)
	}
}