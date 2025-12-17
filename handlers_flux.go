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

// FluxResource holds normalized data for the UI
type FluxResource struct {
	Cluster     string
	Namespace   string
	Name        string
	Type        string // GitRepo, Kustomization, HelmRelease
	Status      string // Ready, Reconciling, Failed
	Message     string
	Revision    string
	LastUpdated string
	Age         string
}

// handleGetFlux gathers Flux CD resources from all clusters
func handleGetFlux(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		selectedCount, queryString, cacheBuster := getRequestFilter(c)
		
		// 1. Find Config Files
		files, err := filepath.Glob(pattern)
		if err != nil {
			return c.String(500, "Error finding kubeconfig files")
		}

		base := PageBase{
			Title:                "Flux CD Overview",
			ActivePage:           "flux",
			SelectedClusterCount: selectedCount,
			QueryString:          queryString,
			CacheBuster:          cacheBuster,
			LastRefreshed:        time.Now().Format(time.RFC1123),
			IsAdmin:              CurrentConfig.IsAdmin,
		}

		// 2. Define the Flux CRDs we want to fetch
		// Note: API Versions (v1/v1beta2) might vary by cluster age, 
		// but v1 is standard for modern Flux.
		gvrGit := schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
		gvrKust := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
		gvrHelm := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2beta1", Resource: "helmreleases"} // v2beta1 or v2beta2 usually

		var allResources []FluxResource
		var mutex sync.Mutex
		var wg sync.WaitGroup

		// 3. Parallel Fetch
		for _, configFile := range files {
			wg.Add(1)
			go func(file string) {
				defer wg.Done()

				// Build Dynamic Client
				config, err := clientcmd.BuildConfigFromFlags("", file)
				if err != nil {
					return
				}
				// [Optimization] Increase QPS for discovery
				config.QPS = 20
				config.Burst = 50
				
				dynClient, err := dynamic.NewForConfig(config)
				if err != nil {
					return
				}
				
				// Get Context Name for UI
				rawConfig, _ := clientcmd.LoadFromFile(file)
				clusterName := filepath.Base(file)
				if rawConfig != nil && rawConfig.CurrentContext != "" {
					clusterName = rawConfig.CurrentContext
				}

				// Helper to fetch GVR
				fetchGVR := func(gvr schema.GroupVersionResource, typeLabel string) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()

					list, err := dynClient.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
					if err != nil {
						// CRD might not exist on this cluster, skip silently
						return
					}

					for _, item := range list.Items {
						// Extract Unstructured Data
						name := item.GetName()
						ns := item.GetNamespace()
						creation := item.GetCreationTimestamp().Time
						
						// Dig into status fields safely
						statusMap, found, _ := unstructured.NestedMap(item.Object, "status")
						statusStr := "Unknown"
						message := ""
						revision := ""
						
						if found {
							// 1. Get Conditions to determine "Ready" state
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
										} else {
											statusStr = "Unknown"
										}
										break
									}
								}
							}
							
							// 2. Get specific revision fields based on type
							// FIXED: unstructured.NestedString returns 3 values (val, found, error)
							if typeLabel == "GitRepo" {
								if rev, ok, _ := unstructured.NestedString(item.Object, "status", "artifact", "revision"); ok {
									revision = rev
								}
							} else if typeLabel == "Kustomization" {
								if rev, ok, _ := unstructured.NestedString(item.Object, "status", "lastAppliedRevision"); ok {
									revision = rev
								}
							} else if typeLabel == "HelmRelease" {
								if rev, ok, _ := unstructured.NestedString(item.Object, "status", "lastAppliedRevision"); ok {
									revision = rev
								}
							}
						}

						res := FluxResource{
							Cluster:     clusterName,
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

				// Fetch all 3 types
				fetchGVR(gvrGit, "GitRepo")
				fetchGVR(gvrKust, "Kustomization")
				fetchGVR(gvrHelm, "HelmRelease")

			}(configFile)
		}

		wg.Wait()

		// Sort: Failed first, then by Cluster
		sort.Slice(allResources, func(i, j int) bool {
			if allResources[i].Status == "Failed" && allResources[j].Status != "Failed" { return true }
			if allResources[i].Status != "Failed" && allResources[j].Status == "Failed" { return false }
			return allResources[i].Cluster < allResources[j].Cluster
		})

		data := struct {
			PageBase  PageBase
			Resources []FluxResource
		}{
			PageBase:  base,
			Resources: allResources,
		}

		return c.Render(200, "flux.html", data)
	}
}