package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- Handlers ---

// handleGetDeployments lists all deployments aggregated by name
func handleGetDeployments(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		base := GetBaseData(c, "Deployments", "deployments")

		configsToProcess, err := getConfigsToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Error finding configs")
		}

		clients, clientErrors := createClients(configsToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)

		type depResult struct {
			ClusterName string
			Items       []appsv1.Deployment
		}

		fetchDeps := func(client KubeClient) (depResult, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			list, err := client.Clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
			if err != nil {
				return depResult{}, err
			}
			return depResult{ClusterName: client.ContextName, Items: list.Items}, nil
		}

		results, fetchErrors := ParallelFetch(clients, fetchDeps)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		// Aggregation Logic
		aggMap := make(map[string]*AggregatedDeploymentView)
		clusterStats := make(map[string]int)

		type nsHealth struct {
			count      int
			hasFailure bool
			hasWarning bool
		}
		nsHealthMap := make(map[string]*nsHealth)

		for _, res := range results {
			clusterStats[res.ClusterName] = len(res.Items)
			for _, d := range res.Items {
				if _, ok := aggMap[d.Name]; !ok {
					aggMap[d.Name] = &AggregatedDeploymentView{Name: d.Name}
				}
				entry := aggMap[d.Name]

				entry.TotalReadyReplicas += int(d.Status.ReadyReplicas)
				if d.Spec.Replicas != nil {
					entry.TotalDesiredReplicas += int(*d.Spec.Replicas)
				} else {
					entry.TotalDesiredReplicas += 1
				}

				// Images
				for _, c := range d.Spec.Template.Spec.Containers {
					found := false
					for _, img := range entry.Images {
						if img == c.Image {
							found = true
							break
						}
					}
					if !found {
						entry.Images = append(entry.Images, c.Image)
					}
				}

				// Strategy
				strat := string(d.Spec.Strategy.Type)
				foundStrat := false
				for _, s := range entry.Strategies {
					if s == strat {
						foundStrat = true
						break
					}
				}
				if !foundStrat {
					entry.Strategies = append(entry.Strategies, strat)
				}

				// Clusters & Namespaces
				foundClus := false
				for _, cl := range entry.Clusters {
					if cl == res.ClusterName {
						foundClus = true
						break
					}
				}
				if !foundClus {
					entry.Clusters = append(entry.Clusters, res.ClusterName)
				}

				foundNs := false
				for _, ns := range entry.Namespaces {
					if ns == d.Namespace {
						foundNs = true
						break
					}
				}
				if !foundNs {
					entry.Namespaces = append(entry.Namespaces, d.Namespace)
				}

				// Namespace Health Stats
				if _, ok := nsHealthMap[d.Namespace]; !ok {
					nsHealthMap[d.Namespace] = &nsHealth{}
				}
				h := nsHealthMap[d.Namespace]
				h.count++

				desired := int32(1)
				if d.Spec.Replicas != nil {
					desired = *d.Spec.Replicas
				}

				if desired > 0 {
					if d.Status.ReadyReplicas == 0 {
						h.hasFailure = true
					} else if d.Status.ReadyReplicas < desired {
						h.hasWarning = true
					}
				}
			}
		}

		var allDeps []AggregatedDeploymentView
		for _, v := range aggMap {
			allDeps = append(allDeps, *v)
		}
		sort.Slice(allDeps, func(i, j int) bool { return allDeps[i].Name < allDeps[j].Name })

		var cStats []ClusterStat
		for k, v := range clusterStats {
			cStats = append(cStats, ClusterStat{Name: k, Count: v})
		}
		sort.Slice(cStats, func(i, j int) bool { return cStats[i].Name < cStats[j].Name })

		var nStats []NamespaceStat
		for k, v := range nsHealthMap {
			color := "#10b981" // Green
			detail := "Healthy"
			if v.hasFailure {
				color = "#ef4444" // Red
				detail = "Has Failures"
			} else if v.hasWarning {
				color = "#f59e0b" // Yellow
				detail = "Degraded"
			}
			nStats = append(nStats, NamespaceStat{Name: k, Count: v.count, Color: color, ErrorDetail: detail})
		}
		sort.Slice(nStats, func(i, j int) bool { return nStats[i].Count > nStats[j].Count })

		return c.Render(200, "deployments.html", DeploymentPageData{
			PageBase:               base,
			Deployments:            allDeps,
			TotalUniqueDeployments: len(allDeps),
			ClusterStats:           cStats,
			NamespaceStats:         nStats,
		})
	}
}

// handleGetDeploymentDetail fetches details including ReplicaSets (History)
func handleGetDeploymentDetail(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		deployName := c.QueryParam("name")
		base := GetBaseData(c, deployName, "deployments")

		configsToProcess, err := getConfigsToProcess(c, pattern)
		if err != nil {
			return c.String(500, "Config error")
		}
		clients, clientErrors := createClients(configsToProcess)
		base.ErrorLogs = append(base.ErrorLogs, clientErrors...)

		type detailResult struct {
			ClusterName string
			Overviews   map[string]DeploymentDetailView
			Pods        map[string][]PodInfo
		}

		fetchDetail := func(client KubeClient) (detailResult, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			depList, err := client.Clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
			if err != nil {
				return detailResult{}, err
			}

			overviews := make(map[string]DeploymentDetailView)
			podsMap := make(map[string][]PodInfo)
			// Ensure map is initialized for the namespace even if empty, so UI renders the table container

			for _, d := range depList.Items {
				if d.Name != deployName {
					continue
				}

				// Fetch ReplicaSets
				rsList, _ := client.Clientset.AppsV1().ReplicaSets(d.Namespace).List(ctx, metav1.ListOptions{})
				var history []RolloutHistoryInfo
				validRSUIDs := make(map[string]bool)

				for _, rs := range rsList.Items {
					if metav1.IsControlledBy(&rs, &d) {
						validRSUIDs[string(rs.UID)] = true
						var imgs []string
						for _, c := range rs.Spec.Template.Spec.Containers {
							imgs = append(imgs, c.Image)
						}
						cause := rs.Annotations["kubernetes.io/change-cause"]
						if cause == "" {
							cause = "<none>"
						}

						revStr := rs.Annotations["deployment.kubernetes.io/revision"]
						revInt, _ := strconv.ParseInt(revStr, 10, 64)

						replicas := int32(0)
						if rs.Spec.Replicas != nil {
							replicas = *rs.Spec.Replicas
						}

						history = append(history, RolloutHistoryInfo{
							Name:        rs.Name,
							Revision:    revInt,
							Replicas:    fmt.Sprintf("%d", replicas),
							IsActive:    rs.Status.Replicas > 0,
							ChangeCause: cause,
							Age:         formatAge(rs.CreationTimestamp),
							Images:      imgs,
						})
					}
				}
				sort.Slice(history, func(i, j int) bool {
					return history[i].Revision > history[j].Revision
				})

				// Build Overview
				var imgs []string
				for _, c := range d.Spec.Template.Spec.Containers {
					imgs = append(imgs, c.Image)
				}
				var conds []string
				for _, c := range d.Status.Conditions {
					conds = append(conds, fmt.Sprintf("%s: %s (%s)", c.Type, c.Status, c.Reason))
				}

				var desired int32 = 1
				if d.Spec.Replicas != nil {
					desired = *d.Spec.Replicas
				}

				overviews[d.Name] = DeploymentDetailView{
					Status:         fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, desired),
					Strategy:       string(d.Spec.Strategy.Type),
					Selector:       metav1.FormatLabelSelector(d.Spec.Selector),
					Images:         imgs,
					Conditions:     conds,
					RolloutHistory: history,
					RolloutStatus:  getRolloutStatus(d),
				}

				// Fetch Pods
				// Initialize the slice so the key exists in the map (important for UI to render empty table)
				podsMap[d.Namespace] = []PodInfo{}

				podList, _ := client.Clientset.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{
					LabelSelector: metav1.FormatLabelSelector(d.Spec.Selector),
				})
				for _, p := range podList.Items {
					// Filter: Ensure pod is owned by one of the deployment's ReplicaSets
					isOwned := false
					for _, owner := range p.OwnerReferences {
						if validRSUIDs[string(owner.UID)] {
							isOwned = true
							break
						}
					}
					if !isOwned {
						continue
					}

					readyCount := 0
					restartCount := 0
					for _, cs := range p.Status.ContainerStatuses {
						if cs.Ready {
							readyCount++
						}
						restartCount += int(cs.RestartCount)
					}

					displayStatus := string(p.Status.Phase)
					if p.DeletionTimestamp != nil {
						displayStatus = "Terminating"
					} else {
						for _, cs := range p.Status.ContainerStatuses {
							if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
								displayStatus = cs.State.Waiting.Reason
								break
							}
							if cs.State.Terminated != nil && cs.State.Terminated.Reason != "Completed" {
								displayStatus = cs.State.Terminated.Reason
								break
							}
						}
					}

					podsMap[d.Namespace] = append(podsMap[d.Namespace], PodInfo{
						Name:      p.Name,
						Ready:     fmt.Sprintf("%d/%d", readyCount, len(p.Spec.Containers)),
						Status:    displayStatus,
						Restarts:  restartCount,
						Node:      p.Spec.NodeName,
						Cluster:   client.ContextName,
						Namespace: d.Namespace,
					})
				}
			}

			return detailResult{ClusterName: client.ContextName, Overviews: overviews, Pods: podsMap}, nil
		}

		results, fetchErrors := ParallelFetch(clients, fetchDetail)
		base.ErrorLogs = append(base.ErrorLogs, fetchErrors...)

		data := DeploymentDetailPageData{
			PageBase:       base,
			DeploymentName: deployName,
			Overviews:      make(map[string]map[string]DeploymentDetailView),
			Pods:           make(map[string]map[string][]PodInfo),
		}

		var clusterNames []string
		for _, res := range results {
			if len(res.Overviews) > 0 {
				clusterNames = append(clusterNames, res.ClusterName)
				data.Overviews[res.ClusterName] = res.Overviews
				data.Pods[res.ClusterName] = res.Pods
			}
		}
		sort.Strings(clusterNames)
		data.ClusterNames = clusterNames

		return c.Render(200, "deployment-detail.html", data)
	}
}

// DeploymentDetailAPIResponse is the JSON response for the API
type DeploymentDetailAPIResponse struct {
	ClusterName string
	Namespace   string
	Overview    DeploymentDetailView
	Pods        map[string][]PodInfo
}

// handleGetDeploymentDetailAPI returns JSON data for the deployment detail page
func handleGetDeploymentDetailAPI(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Prevent Caching to ensure real-time updates
		c.Response().Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		c.Response().Header().Set("Pragma", "no-cache")
		c.Response().Header().Set("Expires", "0")

		deployName := c.QueryParam("name")
		if deployName == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Missing name parameter"})
		}

		configsToProcess, err := getConfigsToProcess(c, pattern)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Config error"})
		}
		clients, _ := createClients(configsToProcess)

		type detailResult struct {
			Items []DeploymentDetailAPIResponse
		}

		fetchDetail := func(client KubeClient) (detailResult, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			depList, err := client.Clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
			if err != nil {
				return detailResult{}, err
			}

			var items []DeploymentDetailAPIResponse

			for _, d := range depList.Items {
				if d.Name != deployName {
					continue
				}

				// Fetch ReplicaSets
				rsList, _ := client.Clientset.AppsV1().ReplicaSets(d.Namespace).List(ctx, metav1.ListOptions{})
				var history []RolloutHistoryInfo
				validRSUIDs := make(map[string]bool)

				for _, rs := range rsList.Items {
					if metav1.IsControlledBy(&rs, &d) {
						validRSUIDs[string(rs.UID)] = true
						var imgs []string
						for _, c := range rs.Spec.Template.Spec.Containers {
							imgs = append(imgs, c.Image)
						}
						cause := rs.Annotations["kubernetes.io/change-cause"]
						if cause == "" {
							cause = "<none>"
						}

						revStr := rs.Annotations["deployment.kubernetes.io/revision"]
						revInt, _ := strconv.ParseInt(revStr, 10, 64)

						replicas := int32(0)
						if rs.Spec.Replicas != nil {
							replicas = *rs.Spec.Replicas
						}

						history = append(history, RolloutHistoryInfo{
							Name:        rs.Name,
							Revision:    revInt,
							Replicas:    fmt.Sprintf("%d", replicas),
							IsActive:    rs.Status.Replicas > 0,
							ChangeCause: cause,
							Age:         formatAge(rs.CreationTimestamp),
							Images:      imgs,
						})
					}
				}
				sort.Slice(history, func(i, j int) bool {
					return history[i].Revision > history[j].Revision
				})

				// Build Overview
				var imgs []string
				for _, c := range d.Spec.Template.Spec.Containers {
					imgs = append(imgs, c.Image)
				}
				var conds []string
				for _, c := range d.Status.Conditions {
					conds = append(conds, fmt.Sprintf("%s: %s (%s)", c.Type, c.Status, c.Reason))
				}

				var desired int32 = 1
				if d.Spec.Replicas != nil {
					desired = *d.Spec.Replicas
				}

				view := DeploymentDetailView{
					Status:         fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, desired),
					Strategy:       string(d.Spec.Strategy.Type),
					Selector:       metav1.FormatLabelSelector(d.Spec.Selector),
					Images:         imgs,
					Conditions:     conds,
					RolloutHistory: history,
					RolloutStatus:  getRolloutStatus(d),
				}

				// Fetch Pods
				podsMap := make(map[string][]PodInfo)
				podsMap[d.Namespace] = []PodInfo{} // Initialize key

				podList, _ := client.Clientset.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{
					LabelSelector: metav1.FormatLabelSelector(d.Spec.Selector),
				})
				for _, p := range podList.Items {
					// Filter: Ensure pod is owned by one of the deployment's ReplicaSets
					isOwned := false
					for _, owner := range p.OwnerReferences {
						if validRSUIDs[string(owner.UID)] {
							isOwned = true
							break
						}
					}
					if !isOwned {
						continue
					}

					readyCount := 0
					restartCount := 0
					for _, cs := range p.Status.ContainerStatuses {
						if cs.Ready {
							readyCount++
						}
						restartCount += int(cs.RestartCount)
					}

					displayStatus := string(p.Status.Phase)
					if p.DeletionTimestamp != nil {
						displayStatus = "Terminating"
					} else {
						for _, cs := range p.Status.ContainerStatuses {
							if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
								displayStatus = cs.State.Waiting.Reason
								break
							}
							if cs.State.Terminated != nil && cs.State.Terminated.Reason != "Completed" {
								displayStatus = cs.State.Terminated.Reason
								break
							}
						}
					}

					podsMap[d.Namespace] = append(podsMap[d.Namespace], PodInfo{
						Name:      p.Name,
						Ready:     fmt.Sprintf("%d/%d", readyCount, len(p.Spec.Containers)),
						Status:    displayStatus,
						Restarts:  restartCount,
						Node:      p.Spec.NodeName,
						Cluster:   client.ContextName,
						Namespace: d.Namespace,
					})
				}

				items = append(items, DeploymentDetailAPIResponse{
					ClusterName: client.ContextName,
					Namespace:   d.Namespace,
					Overview:    view,
					Pods:        podsMap,
				})
			}

			return detailResult{Items: items}, nil
		}

		results, _ := ParallelFetch(clients, fetchDetail)

		var response []DeploymentDetailAPIResponse
		for _, res := range results {
			response = append(response, res.Items...)
		}

		return c.JSON(http.StatusOK, response)
	}
}

// Helper to determine rollout status
func getRolloutStatus(d appsv1.Deployment) RolloutStatusInfo {
	if d.Generation <= d.Status.ObservedGeneration {
		cond := getDeploymentCondition(d.Status, appsv1.DeploymentProgressing)
		if cond != nil && cond.Reason == "ProgressDeadlineExceeded" {
			return RolloutStatusInfo{IsComplete: false, Message: fmt.Sprintf("Deployment %q exceeded its progress deadline", d.Name)}
		}
		if d.Spec.Replicas != nil && d.Status.UpdatedReplicas < *d.Spec.Replicas {
			return RolloutStatusInfo{IsComplete: false, Message: fmt.Sprintf("Waiting for rollout to finish: %d out of %d new replicas have been updated...", d.Status.UpdatedReplicas, *d.Spec.Replicas)}
		}
		if d.Status.Replicas > d.Status.UpdatedReplicas {
			return RolloutStatusInfo{IsComplete: false, Message: fmt.Sprintf("Waiting for rollout to finish: %d old replicas are pending termination...", d.Status.Replicas-d.Status.UpdatedReplicas)}
		}
		if d.Status.AvailableReplicas < d.Status.UpdatedReplicas {
			return RolloutStatusInfo{IsComplete: false, Message: fmt.Sprintf("Waiting for rollout to finish: %d of %d updated replicas are available...", d.Status.AvailableReplicas, d.Status.UpdatedReplicas)}
		}
		return RolloutStatusInfo{IsComplete: true, Message: "Deployment successfully rolled out."}
	}
	return RolloutStatusInfo{IsComplete: false, Message: "Waiting for rollout to finish: observed generation is less than spec generation..."}
}

func getDeploymentCondition(status appsv1.DeploymentStatus, condType appsv1.DeploymentConditionType) *appsv1.DeploymentCondition {
	for i := range status.Conditions {
		c := status.Conditions[i]
		if c.Type == condType {
			return &c
		}
	}
	return nil
}
