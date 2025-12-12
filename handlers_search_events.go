package main

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// handleSearch renders the search page shell immediately
func handleSearch(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		
		base := PageBase{
			Title:                "Search",
			ActivePage:           "search",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			IsSearchPage:         true,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsAdmin:              CurrentConfig.IsAdmin,
		}
		
		// If query exists, we pass it to the view so the input box is pre-filled,
		// but we do NOT execute the search here. The frontend will trigger it.
		data := SearchPageData{
			PageBase: base,
			Query:    c.QueryParam("q"),
		}

		return c.Render(200, "search.html", data)
	}
}

// handleSearchAPI executes a search for a specific resource type and returns JSON
func handleSearchAPI(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		query := c.QueryParam("q")
		resourceType := c.QueryParam("type")
		
		if query == "" {
			return c.JSON(http.StatusOK, []SearchResult{})
		}

		re, err := regexp.Compile(query)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid Regex"})
		}

		// Prepare Clients (We do this per request to respect current file state)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Config Error"})
		}
		clients, _ := createClients(filesToProcess) // We ignore client errors for the API to keep it simple

		resultsChan := make(chan SearchResult, 100)
		var wg sync.WaitGroup

		wg.Add(1)
		
		// Dispatch to the correct helper based on type
        // (These functions are now defined in helpers.go)
		switch resourceType {
		case "cluster":
			go searchClusters(re, clients, resultsChan, &wg)
		case "namespace":
			go searchNamespaces(re, clients, resultsChan, &wg)
		case "deployment":
			go searchDeployments(re, clients, resultsChan, &wg)
		case "pod":
			go searchPods(re, clients, resultsChan, &wg)
		case "replicaset":
			go searchReplicaSets(re, clients, resultsChan, &wg)
		case "daemonset":
			go searchDaemonSets(re, clients, resultsChan, &wg)
		case "statefulset":
			go searchStatefulSets(re, clients, resultsChan, &wg)
		case "configmap":
			go searchConfigMaps(re, clients, resultsChan, &wg)
		case "node":
			go searchNodes(re, clients, resultsChan, &wg)
		case "pv":
			go searchPersistentVolumes(re, clients, resultsChan, &wg)
		case "pvc":
			go searchPVCs(re, clients, resultsChan, &wg)
		case "service":
			go searchServices(re, clients, resultsChan, &wg)
		case "ingress":
			go searchIngresses(re, clients, resultsChan, &wg)
		case "secret":
			// Security check for secrets
			if CurrentConfig.IsAdmin {
				go searchSecrets(re, clients, resultsChan, &wg)
			} else {
				wg.Done()
			}
		case "serviceaccount":
			go searchServiceAccounts(re, clients, resultsChan, &wg)
		default:
			wg.Done() // Invalid type, just return empty
		}

		// Closer routine
		go func() {
			wg.Wait()
			close(resultsChan)
		}()

		// Collect results
		var results []SearchResult
		for res := range resultsChan {
			results = append(results, res)
		}

		return c.JSON(http.StatusOK, results)
	}
}

// handleGetEvents lists all events from the last hour
func handleGetEvents(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}

		now := time.Now().UTC()
		hourAgo := now.Add(-1 * time.Hour)

		base := PageBase{
			Title:                "Cluster Events (Last Hour)",
			ActivePage:           "events",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        now.Format(time.RFC1123),
			IsSearchPage:         false,
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)

		if len(filesToProcess) == 0 {
			base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("No clusters selected or found matching pattern '%s'", pattern))
		}

		type eventFetchResult struct {
			ClusterName string
			Events      []EventInfo
		}

		fetchEvents := func(client KubeClient) (eventFetchResult, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			
			eventList, err := client.Clientset.CoreV1().Events("").List(ctx, metav1.ListOptions{})
			if err != nil {
				return eventFetchResult{}, err
			}

			var filtered []EventInfo
			for _, e := range eventList.Items {
				if e.LastTimestamp.Time.Before(hourAgo) {
					continue 
				}
				filtered = append(filtered, EventInfo{
					Cluster:   client.ContextName,
					Namespace: e.InvolvedObject.Namespace,
					Object:    fmt.Sprintf("%s/%s", e.InvolvedObject.Kind, e.InvolvedObject.Name),
					Type:      e.Type,
					Reason:    e.Reason,
					Message:   e.Message,
					Count:     int(e.Count),
					LastSeen:  formatAge(e.LastTimestamp),
					Timestamp: e.LastTimestamp.Time,
				})
			}
			return eventFetchResult{ClusterName: client.ContextName, Events: filtered}, nil
		}

		results, fetchErrors := ParallelFetch(clients, fetchEvents)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		var allEvents []EventInfo
		clusterDistribution := make(map[string]int)
		namespaceDistribution := make(map[string]int)
		reasonDistribution := make(map[string]int)
		var clusterNames []string

		for _, res := range results {
			clusterNames = append(clusterNames, res.ClusterName)
			for _, e := range res.Events {
				allEvents = append(allEvents, e)
				if e.Type == "Warning" {
					clusterDistribution[res.ClusterName]++
					if e.Namespace != "" {
						namespaceDistribution[e.Namespace]++
					}
					if e.Reason != "" {
						reasonDistribution[e.Reason]++
					}
				}
			}
		}
		sort.Strings(clusterNames)
		
		sort.Slice(allEvents, func(i, j int) bool {
			return allEvents[i].Timestamp.After(allEvents[j].Timestamp)
		})

		var recentEvents []EventInfo
		if len(allEvents) > 100 {
			recentEvents = allEvents[:100]
		} else {
			recentEvents = allEvents
		}
		
		var clusterStats []ClusterStat; for n, c := range clusterDistribution { clusterStats = append(clusterStats, ClusterStat{Name: n, Count: c}) }; sort.Slice(clusterStats, func(i, j int) bool { return clusterStats[i].Name < clusterStats[j].Name })
		
		const topN = 10
		var namespaceStats []NamespaceStat
		for n, c := range namespaceDistribution {
			namespaceStats = append(namespaceStats, NamespaceStat{Name: n, Count: c})
		}
		sort.Slice(namespaceStats, func(i, j int) bool { return namespaceStats[i].Count > namespaceStats[j].Count })
		if len(namespaceStats) > topN {
			var otherSum int; for _, ns := range namespaceStats[topN:] { otherSum += ns.Count }; namespaceStats = namespaceStats[:topN]; namespaceStats = append(namespaceStats, NamespaceStat{Name: "Others", Count: otherSum})
		}

		var reasonStats []ReasonStat
		for n, c := range reasonDistribution {
			reasonStats = append(reasonStats, ReasonStat{Reason: n, Count: c})
		}
		sort.Slice(reasonStats, func(i, j int) bool { return reasonStats[i].Count > reasonStats[j].Count })
		if len(reasonStats) > topN {
			var otherSum int; for _, o := range reasonStats[topN:] { otherSum += o.Count }; reasonStats = reasonStats[:topN]; reasonStats = append(reasonStats, ReasonStat{Reason: "Others", Count: otherSum})
		}
		
		// Heatmap Calculation
		heatmapData := make(map[string]map[string]map[string]int)
		for _, name := range clusterNames {
			heatmapData[name] = make(map[string]map[string]int)
		}
		
		var heatmapNamespaces []string
		for _, ns := range namespaceStats {
			if ns.Name != "Others" {
				heatmapNamespaces = append(heatmapNamespaces, ns.Name)
			}
		}
		
		for _, e := range allEvents {
			if e.Type != "Warning" { continue }
			inTop := false
			for _, topNS := range heatmapNamespaces {
				if e.Namespace == topNS { inTop = true; break }
			}
			if !inTop { continue }
			
			if _, ok := heatmapData[e.Cluster][e.Namespace]; !ok {
				heatmapData[e.Cluster][e.Namespace] = make(map[string]int)
			}
			heatmapData[e.Cluster][e.Namespace][e.Reason]++
		}

		var heatmapRows []HeatmapRow
		for _, clusterName := range clusterNames {
			row := HeatmapRow{ClusterName: clusterName}
			for _, nsName := range heatmapNamespaces {
				totalCount := 0
				topReason := "None"
				topReasonCount := 0

				if nsData, ok := heatmapData[clusterName]; ok {
					if reasonData, ok := nsData[nsName]; ok {
						for reason, count := range reasonData {
							totalCount += count
							if count > topReasonCount {
								topReasonCount = count
								topReason = reason
							}
						}
					}
				}
				
				// getHeatLevel is defined in helpers.go
				row.Cells = append(row.Cells, HeatmapCell{
					Count:          totalCount,
					Level:          getHeatLevel(totalCount),
					TopReason:      topReason,
					TopReasonCount: topReasonCount,
				})
			}
			heatmapRows = append(heatmapRows, row)
		}

		data := EventPageData{
			PageBase:          base,
			RecentEvents:      recentEvents,
			TotalEvents:       len(allEvents),
			ClusterStats:      clusterStats,
			NamespaceStats:    namespaceStats,
			ReasonStats:       reasonStats,
			HeatmapNamespaces: heatmapNamespaces,
			HeatmapRows:       heatmapRows,
		}

		return c.Render(200, "events.html", data)
	}
}