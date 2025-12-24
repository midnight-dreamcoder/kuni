package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/labstack/echo/v4"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// handleGetCRDs lists all Custom Resource Definitions across clusters
func handleGetCRDs(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		configsToProcess, err := getConfigsToProcess(c, pattern)
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
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		clients, clientErrors := createClients(configsToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)

		// --- 1. Define Fetch Logic ---
		type crdFetchResult struct {
			ClusterName string
			Items       []apiextensionsv1.CustomResourceDefinition
		}

		fetchCRDs := func(client KubeClient) (crdFetchResult, error) {
			// CRDs require the extensions clientset, not the standard one
			config, err := clientcmd.BuildConfigFromFlags("", client.ConfigPath)
			if err != nil {
				return crdFetchResult{}, fmt.Errorf("config error: %v", err)
			}
			
			extClient, err := clientset.NewForConfig(config)
			if err != nil {
				return crdFetchResult{}, fmt.Errorf("client error: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			
			list, err := extClient.ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
			if err != nil {
				return crdFetchResult{}, err
			}
			
			return crdFetchResult{ClusterName: client.ContextName, Items: list.Items}, nil
		}

		// --- 2. Execute ---
		results, fetchErrors := ParallelFetch(clients, fetchCRDs)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		// --- 3. Aggregate ---
		crdMap := make(map[string]*CRDInfo) // key = CRD Name
		groupDist := make(map[string]int)
		scopeDist := make(map[string]int)

		for _, res := range results {
			for _, item := range res.Items {
				groupDist[item.Spec.Group]++
				scopeDist[string(item.Spec.Scope)]++

				if existing, ok := crdMap[item.Name]; ok {
					existing.Clusters = append(existing.Clusters, res.ClusterName)
				} else {
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
						Clusters: []string{res.ClusterName},
					}
				}
			}
		}

		// --- 4. Sort & Format ---
		var allCRDs []CRDInfo
		for _, info := range crdMap {
			sort.Strings(info.Clusters)
			allCRDs = append(allCRDs, *info)
		}
		sort.Slice(allCRDs, func(i, j int) bool { return allCRDs[i].Name < allCRDs[j].Name })

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