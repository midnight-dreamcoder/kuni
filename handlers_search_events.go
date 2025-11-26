package main

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// handleSearch performs a global regex search across all resources
func handleSearch(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		allFiles, err := filepath.Glob(pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}
		base := PageBase{
			Title:                "Search",
			ActivePage:           "search",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			IsSearchPage:         true,
			LastRefreshed:        time.Now().Format(time.RFC1123),
		}
		data := SearchPageData{
			PageBase: base,
		}
		query := c.QueryParam("q")
		data.Query = query
		if query == "" {
			return c.Render(200, "search.html", data)
		}
		re, reErr := regexp.Compile(query)
		if reErr != nil {
			data.Error = fmt.Sprintf("Invalid Regex: %v", reErr)
			return c.Render(200, "search.html", data)
		}
		clients, clientErrors := createClients(allFiles)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)
		var wg sync.WaitGroup
		resultsChan := make(chan SearchResult, 100)

		wg.Add(12) 
		go searchClusters(re, clients, resultsChan, &wg)
		go searchNamespaces(re, clients, resultsChan, &wg)
		go searchDeployments(re, clients, resultsChan, &wg)
		go searchPods(re, clients, resultsChan, &wg)
		go searchReplicaSets(re, clients, resultsChan, &wg)
		go searchDaemonSets(re, clients, resultsChan, &wg)
		go searchStatefulSets(re, clients, resultsChan, &wg)
		go searchConfigMaps(re, clients, resultsChan, &wg)
		go searchNodes(re, clients, resultsChan, &wg)
		go searchPersistentVolumes(re, clients, resultsChan, &wg)
		go searchPVCs(re, clients, resultsChan, &wg)
		go searchIngresses(re, clients, resultsChan, &wg)

		go func() {
			wg.Wait()
			close(resultsChan)
		}()

		aggregator := make(map[string]SearchResult)
		for res := range resultsChan {
			key := fmt.Sprintf("%s-%s-%s-%s", res.Type, res.Cluster, res.Namespace, res.Name)
			if existing, ok := aggregator[key]; ok {
				existing.Matches = append(existing.Matches, res.Matches...)
				aggregator[key] = existing
			} else {
				aggregator[key] = res
			}
		}
		for _, res := range aggregator {
			data.Results = append(data.Results, res)
		}

		sort.Slice(data.Results, func(i, j int) bool {
			if data.Results[i].Type != data.Results[j].Type {
				return data.Results[i].Type < data.Results[j].Type
			}
			return data.Results[i].Name < data.Results[j].Name
		})

		return c.Render(200, "search.html", data)
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
		}

		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)

		var allEvents []EventInfo
		clusterDistribution := make(map[string]int)
		namespaceDistribution := make(map[string]int)
		reasonDistribution := make(map[string]int)

		var clusterNames []string
		for _, client := range clients {
			clusterNames = append(clusterNames, client.ContextName)
		}
		sort.Strings(clusterNames)

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
				
				eventList, err := client.Clientset.CoreV1().Events("").List(ctx, metav1.ListOptions{})
				if err != nil {
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error: Failed to list events (%v)", client.ContextName, err))
					mutex.Unlock()
					return
				}

				mutex.Lock()
				for _, e := range eventList.Items {
					if e.LastTimestamp.Time.Before(hourAgo) {
						continue 
					}

					allEvents = append(allEvents, EventInfo{
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

					if e.Type == "Warning" {
						clusterDistribution[client.ContextName]++
						if e.InvolvedObject.Namespace != "" {
							namespaceDistribution[e.InvolvedObject.Namespace]++
						}
						if e.Reason != "" {
							reasonDistribution[e.Reason]++
						}
					}
				}
				mutex.Unlock()
			}(client)
		}
		wg.Wait()
		
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
		
		// --- HEATMAP DATA RE-CALCULATION ---
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