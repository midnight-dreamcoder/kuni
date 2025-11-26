package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// handleGetCRDs lists all Custom Resource Definitions across clusters
func handleGetCRDs(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		filesToProcess, err := getFilesToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}
		base := PageBase{
			Title:                "Custom Resources",
			ActivePage:           "crds",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsSearchPage:         false,
		}
		clients, clientErrors := createClients(filesToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)

		// Aggregators
		crdMap := make(map[string]*CRDInfo) // key = CRD Name
		groupDist := make(map[string]int)
		scopeDist := make(map[string]int)

		var wg sync.WaitGroup
		var mutex sync.Mutex

		for _, client := range clients {
			wg.Add(1)
			go func(client KubeClient) {
				defer wg.Done()
				
				// 1. Build Config from path (needed for Extension Client)
				config, err := clientcmd.BuildConfigFromFlags("", client.ConfigPath)
				if err != nil {
					return 
				}
				
				// 2. Create Extension Client
				extClient, err := clientset.NewForConfig(config)
				if err != nil {
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Error creating CRD client: %v", client.ContextName, err))
					mutex.Unlock()
					return
				}

				// 3. List CRDs
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				
				list, err := extClient.ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
				if err != nil {
					// Try v1beta1 if v1 fails (for older clusters), but usually v1 is standard now
					mutex.Lock()
					base.ErrorLogs = append(base.ErrorLogs, fmt.Sprintf("Cluster: %s | Failed to list CRDs: %v", client.ContextName, err))
					mutex.Unlock()
					return
				}

				mutex.Lock()
				for _, item := range list.Items {
					// Update distributions
					groupDist[item.Spec.Group]++
					scopeDist[string(item.Spec.Scope)]++

					// Deduplicate CRDs across clusters
					if existing, ok := crdMap[item.Name]; ok {
						existing.Clusters = append(existing.Clusters, client.ContextName)
					} else {
						// Find the stored version
						version := ""
						for _, v := range item.Spec.Versions {
							if v.Storage {
								version = v.Name
								break
							}
						}
						
						crdMap[item.Name] = &CRDInfo{
							Name:     item.Name,
							Group:    item.Spec.Group,
							Version:  version,
							Kind:     item.Spec.Names.Kind,
							Scope:    string(item.Spec.Scope),
							Age:      formatAge(item.CreationTimestamp),
							Clusters: []string{client.ContextName},
						}
					}
				}
				mutex.Unlock()
			}(client)
		}
		wg.Wait()

		// Flatten Map to Slice
		var allCRDs []CRDInfo
		for _, info := range crdMap {
			sort.Strings(info.Clusters) // Sort cluster list
			allCRDs = append(allCRDs, *info)
		}
		sort.Slice(allCRDs, func(i, j int) bool { return allCRDs[i].Name < allCRDs[j].Name })

		// Stats
		var groupStats []ClusterStat
		for g, c := range groupDist {
			groupStats = append(groupStats, ClusterStat{Name: g, Count: c})
		}
		sort.Slice(groupStats, func(i, j int) bool { return groupStats[i].Count > groupStats[j].Count })
		if len(groupStats) > 10 {
			groupStats = groupStats[:10]
		}

		var scopeStats []ClusterStat
		for s, c := range scopeDist {
			scopeStats = append(scopeStats, ClusterStat{Name: s, Count: c})
		}

		data := CRDPageData{
			PageBase:   base,
			CRDs:       allCRDs,
			TotalCRDs:  len(allCRDs),
			GroupStats: groupStats,
			ScopeStats: scopeStats,
		}

		return c.Render(200, "crds.html", data)
	}
}