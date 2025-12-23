package main

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// NOTE: FluxResource and FluxPageData are now in structs.go

func handleGetFlux(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)

		// 1. Parse Selected Clusters (from URL ?c=...)
		selectedParams := c.QueryParams()["c"]
		selectedMap := make(map[string]bool)
		for _, s := range selectedParams {
			selectedMap[s] = true
		}
		
		files, err := filepath.Glob(pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}

		// 2. Prepare Base Data
		base := PageBase{
			Title:                "Flux CD Overview",
			ActivePage:           "flux",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		gvrGit := schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
		gvrKust := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
		gvrHelm := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2beta1", Resource: "helmreleases"}

		var allResources []FluxResource
		var mutex sync.Mutex
		var wg sync.WaitGroup

		// 3. Parallel Fetch
		for _, configFile := range files {
			// FILTER FIX: Check against the Filename (ConfigName), not the Context Name
			configName := filepath.Base(configFile)
			
			// If filter is active and this file isn't selected, skip it
			if len(selectedMap) > 0 && !selectedMap[configName] {
				continue
			}

			wg.Add(1)
			go func(file string, fName string) {
				defer wg.Done()

				// Build Client
				config, err := clientcmd.BuildConfigFromFlags("", file)
				if err != nil { return }
				config.QPS = 20
				config.Burst = 50
				
				dynClient, err := dynamic.NewForConfig(config)
				if err != nil { return }
				
				// Get Display Name (Context Name) for the UI column
				rawConfig, _ := clientcmd.LoadFromFile(file)
				clusterDisplayName := fName
				if rawConfig != nil && rawConfig.CurrentContext != "" {
					clusterDisplayName = rawConfig.CurrentContext
				}

				// Fetch Helper
				fetchGVR := func(gvr schema.GroupVersionResource, typeLabel string) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()

					list, err := dynClient.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
					if err != nil { return }

					for _, item := range list.Items {
						name := item.GetName()
						ns := item.GetNamespace()
						creation := item.GetCreationTimestamp().Time
						
						statusMap, found, _ := unstructured.NestedMap(item.Object, "status")
						statusStr := "Unknown"
						message := ""
						revision := ""
						
						if found {
							conditions, foundCond, _ := unstructured.NestedSlice(statusMap, "conditions")
							if foundCond {
								for _, cond := range conditions {
									cMap, ok := cond.(map[string]interface{})
									if !ok { continue }
									if cMap["type"] == "Ready" {
										if cMap["status"] == "True" {
											statusStr = "Ready"
										} else if cMap["status"] == "False" {
											statusStr = "Failed"
											if msg, ok := cMap["message"].(string); ok {
												message = msg
											}
										}
										break
									}
								}
							}
							if typeLabel == "GitRepo" {
								if rev, ok, _ := unstructured.NestedString(item.Object, "status", "artifact", "revision"); ok {
									revision = rev
								}
							} else if typeLabel == "Kustomization" || typeLabel == "HelmRelease" {
								if rev, ok, _ := unstructured.NestedString(item.Object, "status", "lastAppliedRevision"); ok {
									revision = rev
								}
							}
						}

						res := FluxResource{
							Cluster:     clusterDisplayName,
							Namespace:   ns,
							Name:        name,
							Type:        typeLabel,
							Status:      statusStr,
							Message:     message,
							Revision:    revision,
							Age:         formatAge(metav1.NewTime(creation)),
						}

						mutex.Lock()
						allResources = append(allResources, res)
						mutex.Unlock()
					}
				}

				fetchGVR(gvrGit, "GitRepo")
				fetchGVR(gvrKust, "Kustomization")
				fetchGVR(gvrHelm, "HelmRelease")

			}(configFile, configName)
		}

		wg.Wait()

		sort.Slice(allResources, func(i, j int) bool {
			if allResources[i].Status == "Failed" && allResources[j].Status != "Failed" { return true }
			if allResources[i].Status != "Failed" && allResources[j].Status == "Failed" { return false }
			return allResources[i].Cluster < allResources[j].Cluster
		})

		// 4. Populate Struct with SelectedClusters
		data := FluxPageData{
			PageBase:         base,
			Resources:        allResources,
			SelectedClusters: selectedMap, // <--- Now matches other pages
		}

		return c.Render(200, "flux.html", data)
	}
}