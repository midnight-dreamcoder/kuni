package main

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/duration"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	metrics "k8s.io/metrics/pkg/client/clientset/versioned"
)

// formatAge is a helper function to create a human-readable "age" string
func formatAge(t metav1.Time) string {
	return duration.HumanDuration(time.Since(t.Time))
}

// formatMemory converts a resource.Quantity to a human-readable Gi/Mi string
func formatMemory(q *resource.Quantity) string {
	if q.IsZero() {
		return "0"
	}
	// Format as Gi with one decimal place
	valGi := float64(q.Value()) / (1024 * 1024 * 1024)
	if valGi >= 1 {
		return fmt.Sprintf("%.1f Gi", valGi)
	}
	// Otherwise, format as Mi
	valMi := float64(q.Value()) / (1024 * 1024)
	return fmt.Sprintf("%.0f Mi", valMi)
}

// formatCpu converts a resource.Quantity to a human-readable core count
func formatCpu(q *resource.Quantity) string {
	if q.IsZero() {
		return "0"
	}
	// Format as cores with 2 decimal places
	return fmt.Sprintf("%.2f", q.AsApproximateFloat64())
}

// getRequestFilter returns the count, clean query, and cache buster
func getRequestFilter(c echo.Context) (int, string, string) {
	selectedClusters := c.QueryParams()["c"]
	uniqueClusters := make(map[string]bool)
	var cleanClusterList []string
	for _, cluster := range selectedClusters {
		if !uniqueClusters[cluster] {
			uniqueClusters[cluster] = true
			cleanClusterList = append(cleanClusterList, cluster)
		}
	}
	selectedCount := len(cleanClusterList)
	var queryString string
	if selectedCount > 0 {
		v := url.Values{}
		for _, cluster := range cleanClusterList {
			v.Add("c", cluster)
		}
		queryString = "?" + v.Encode()
	}
	version := time.Now().UnixNano()
	cacheBuster := fmt.Sprintf("?v=%d", version)
	return selectedCount, queryString, cacheBuster
}

// getFilesToProcess filters config files based on a list of CONTEXT NAMES
func getFilesToProcess(c echo.Context, pattern string) ([]string, error) {
	selectedContexts := c.QueryParams()["c"]
	if len(selectedContexts) == 0 {
		return filepath.Glob(pattern)
	}
	allowedSet := make(map[string]bool)
	for _, name := range selectedContexts {
		allowedSet[name] = true
	}
	allFiles, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	var filesToUse []string
	var mutex sync.Mutex
	var wg sync.WaitGroup
	for _, configFile := range allFiles {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
			loadingRules.ExplicitPath = path
			overrides := &clientcmd.ConfigOverrides{}
			kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
			rawConfig, err := kubeConfig.RawConfig()
			if err != nil {
				return
			}
			if allowedSet[rawConfig.CurrentContext] {
				mutex.Lock()
				filesToUse = append(filesToUse, path)
				mutex.Unlock()
			}
		}(configFile)
	}
	wg.Wait()
	return filesToUse, nil
}

// createClients loops through a list of config files and returns
// a list of usable clients and a list of error messages.
func createClients(files []string) ([]KubeClient, []string) {
	var clients []KubeClient
	var errors []string
	for _, configFile := range files {
		contextName := filepath.Base(configFile)
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		loadingRules.ExplicitPath = configFile
		overrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
		rawConfig, err := kubeConfig.RawConfig()
		if err == nil && rawConfig.CurrentContext != "" {
			contextName = rawConfig.CurrentContext
		}
		config, err := kubeConfig.ClientConfig()
		if err != nil {
			errors = append(errors, fmt.Sprintf("Cluster: %s | Error: Failed to build config (%v)", contextName, err))
			continue
		}
		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			errors = append(errors, fmt.Sprintf("Cluster: %s | Error: Failed to create clientset (%v)", contextName, err))
			continue
		}
		clients = append(clients, KubeClient{
			Clientset:   clientset,
			ContextName: contextName,
			ConfigPath:  configFile,
		})
	}
	return clients, errors
}

// getPodReason extracts the most useful "Reason" from a pod's status
func getPodReason(pod v1.Pod) string {
	if pod.Status.Reason != "" {
		return pod.Status.Reason
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Status != v1.ConditionTrue && cond.Reason != "" {
			return cond.Reason
		}
	}
	return ""
}

// getNodeStatus extracts the Ready status from a node's conditions
func getNodeStatus(node v1.Node) (string, string) {
	for _, cond := range node.Status.Conditions {
		if cond.Type == v1.NodeReady {
			if cond.Status == v1.ConditionTrue {
				return "Ready", ""
			}
			return "NotReady", cond.Reason
		}
	}
	return "Unknown", ""
}

// findClient finds a single clientset based on a context name,
// searching all config files matching the pattern.
func findClient(pattern string, clusterContextName string) (*kubernetes.Clientset, error) {
	allFiles, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("error finding kubeconfig files: %v", err)
	}
	for _, configFile := range allFiles {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		loadingRules.ExplicitPath = configFile
		overrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
		rawConfig, _ := kubeConfig.RawConfig()
		if rawConfig.CurrentContext == clusterContextName {
			config, configErr := kubeConfig.ClientConfig()
			if configErr != nil {
				return nil, fmt.Errorf("error building config for %s: %v", clusterContextName, configErr)
			}
			clientset, err := kubernetes.NewForConfig(config)
			if err != nil {
				return nil, fmt.Errorf("error creating clientset for %s: %v", clusterContextName, err)
			}
			return clientset, nil // Success
		}
	}
	return nil, fmt.Errorf("could not find a valid config for context: %s", clusterContextName)
}

// findMetricsClient finds a single metrics clientset based on a context name
func findMetricsClient(pattern string, clusterContextName string) (*metrics.Clientset, error) {
	allFiles, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("error finding kubeconfig files: %v", err)
	}

	for _, configFile := range allFiles {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		loadingRules.ExplicitPath = configFile
		overrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
		rawConfig, _ := kubeConfig.RawConfig()
		
		if rawConfig.CurrentContext == clusterContextName {
			config, configErr := kubeConfig.ClientConfig()
			if configErr != nil {
				return nil, fmt.Errorf("error building config for %s: %v", clusterContextName, configErr)
			}
			metricsClientset, err := metrics.NewForConfig(config)
			if err != nil {
				return nil, fmt.Errorf("error creating metrics clientset for %s: %v", clusterContextName, err)
			}
			return metricsClientset, nil // Success
		}
	}
	
	return nil, fmt.Errorf("could not find a valid config for context: %s", clusterContextName)
}


// --- Search Helper Functions ---

// searchClusters just checks context names (no API call)
func searchClusters(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	for _, client := range clients {
		if re.MatchString(client.ContextName) {
			results <- SearchResult{
				Type:    "Cluster",
				Name:    client.ContextName,
				Cluster: client.ContextName,
				URL:     fmt.Sprintf("/cluster/detail?cluster_name=%s", url.QueryEscape(client.ContextName)),
				Matches: []MatchInfo{{Field: "Context Name", Value: client.ContextName}},
			}
		}
	}
}

// searchNamespaces performs regex search on namespaces
func searchNamespaces(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var internalWg sync.WaitGroup
	for _, client := range clients {
		internalWg.Add(1)
		go func(client KubeClient) {
			defer internalWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			nsList, err := client.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
			cancel()
			if err != nil {
				return
			}
			for _, ns := range nsList.Items {
				if re.MatchString(ns.Name) {
					results <- SearchResult{
						Type:      "Namespace",
						Name:      ns.Name,
						Cluster:   client.ContextName,
						URL:       fmt.Sprintf("/namespace/detail?name=%s", url.QueryEscape(ns.Name)),
						Status:    string(ns.Status.Phase),
						Matches:   []MatchInfo{{Field: "Name", Value: ns.Name}},
					}
				}
			}
		}(client)
	}
	internalWg.Wait()
}

// searchDeployments performs regex search on deployments
func searchDeployments(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var internalWg sync.WaitGroup
	for _, client := range clients {
		internalWg.Add(1)
		go func(client KubeClient) {
			defer internalWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			depList, err := client.Clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
			cancel()
			if err != nil {
				return
			}
			for _, dep := range depList.Items {
				var matches []MatchInfo
				if re.MatchString(dep.Name) {
					matches = append(matches, MatchInfo{Field: "Name", Value: dep.Name})
				}
				for _, container := range dep.Spec.Template.Spec.Containers {
					if re.MatchString(container.Image) {
						matches = append(matches, MatchInfo{Field: "Image", Value: container.Image})
					}
				}
				if len(matches) > 0 {
					var desiredReplicas int32 = 1
					if dep.Spec.Replicas != nil {
						desiredReplicas = *dep.Spec.Replicas
					}
					readyStr := fmt.Sprintf("%d/%d Ready", dep.Status.ReadyReplicas, desiredReplicas)
					results <- SearchResult{
						Type:      "Deployment",
						Name:      dep.Name,
						Cluster:   client.ContextName,
						Namespace: dep.Namespace,
						URL:       fmt.Sprintf("/deployment/detail?name=%s", url.QueryEscape(dep.Name)),
						Info:      readyStr,
						Matches:   matches,
					}
				}
			}
		}(client)
	}
	internalWg.Wait()
}

// searchPods performs regex search on pods
func searchPods(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var internalWg sync.WaitGroup
	for _, client := range clients {
		internalWg.Add(1)
		go func(client KubeClient) {
			defer internalWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			podList, err := client.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
			cancel()
			if err != nil {
				return
			}
			for _, pod := range podList.Items {
				var matches []MatchInfo
				if re.MatchString(pod.Name) {
					matches = append(matches, MatchInfo{Field: "Name", Value: pod.Name})
				}
				if pod.Status.PodIP != "" && re.MatchString(pod.Status.PodIP) {
					matches = append(matches, MatchInfo{Field: "Pod IP", Value: pod.Status.PodIP})
				}
				if len(matches) > 0 {
					restartCount := 0
					for _, cs := range pod.Status.ContainerStatuses {
						restartCount += int(cs.RestartCount)
					}
					restartStr := fmt.Sprintf("%d Restarts", restartCount)
					results <- SearchResult{
						Type:      "Pod",
						Name:      pod.Name,
						Cluster:   client.ContextName,
						Namespace: pod.Namespace,
						URL:       fmt.Sprintf("/pod/detail?cluster_name=%s&namespace=%s&name=%s", url.QueryEscape(client.ContextName), url.QueryEscape(pod.Namespace), url.QueryEscape(pod.Name)),
						Status:    string(pod.Status.Phase),
						Node:      pod.Spec.NodeName,
						Info:      restartStr,
						Matches:   matches,
					}
				}
			}
		}(client)
	}
	internalWg.Wait()
}

// searchReplicaSets performs regex search on replicasets
func searchReplicaSets(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var internalWg sync.WaitGroup
	for _, client := range clients {
		internalWg.Add(1)
		go func(client KubeClient) {
			defer internalWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			rsList, err := client.Clientset.AppsV1().ReplicaSets("").List(ctx, metav1.ListOptions{})
			cancel()
			if err != nil {
				return
			}
			for _, rs := range rsList.Items {
				if re.MatchString(rs.Name) {
					var desiredReplicas int32 = 1
					if rs.Spec.Replicas != nil {
						desiredReplicas = *rs.Spec.Replicas
					}
					readyStr := fmt.Sprintf("%d/%d Ready", rs.Status.ReadyReplicas, desiredReplicas)
					results <- SearchResult{
						Type:       "ReplicaSet",
						Name:       rs.Name,
						Cluster:    client.ContextName,
						Namespace:  rs.Namespace,
						URL:        fmt.Sprintf("/replicaset/detail?name=%s&namespace=%s&cluster_name=%s", url.QueryEscape(rs.Name), url.QueryEscape(rs.Namespace), url.QueryEscape(client.ContextName)),
						Info:       readyStr,
						Matches:    []MatchInfo{{Field: "Name", Value: rs.Name}},
					}
				}
			}
		}(client)
	}
	internalWg.Wait()
}

// searchDaemonSets performs regex search on daemonsets
func searchDaemonSets(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var internalWg sync.WaitGroup
	for _, client := range clients {
		internalWg.Add(1)
		go func(client KubeClient) {
			defer internalWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			dsList, err := client.Clientset.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{})
			cancel()
			if err != nil {
				return
			}
			for _, ds := range dsList.Items {
				if re.MatchString(ds.Name) {
					readyStr := fmt.Sprintf("%d/%d Ready", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
					results <- SearchResult{
						Type:       "DaemonSet",
						Name:       ds.Name,
						Cluster:    client.ContextName,
						Namespace:  ds.Namespace,
						URL:        fmt.Sprintf("/daemonset/detail?name=%s&namespace=%s&cluster_name=%s", url.QueryEscape(ds.Name), url.QueryEscape(ds.Namespace), url.QueryEscape(client.ContextName)),
						Info:       readyStr,
						Status:     "Running",
						Matches:    []MatchInfo{{Field: "Name", Value: ds.Name}},
					}
				}
			}
		}(client)
	}
	internalWg.Wait()
}

// searchStatefulSets performs regex search on statefulsets
func searchStatefulSets(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var internalWg sync.WaitGroup
	for _, client := range clients {
		internalWg.Add(1)
		go func(client KubeClient) {
			defer internalWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			ssList, err := client.Clientset.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{})
			cancel()
			if err != nil {
				return
			}
			for _, ss := range ssList.Items {
				if re.MatchString(ss.Name) {
					var desiredReplicas int32 = 1
					if ss.Spec.Replicas != nil {
						desiredReplicas = *ss.Spec.Replicas
					}
					readyStr := fmt.Sprintf("%d/%d Ready", ss.Status.ReadyReplicas, desiredReplicas)
					results <- SearchResult{
						Type:       "StatefulSet",
						Name:       ss.Name,
						Cluster:    client.ContextName,
						Namespace:  ss.Namespace,
						URL:        fmt.Sprintf("/statefulset/detail?name=%s&namespace=%s&cluster_name=%s", url.QueryEscape(ss.Name), url.QueryEscape(ss.Namespace), url.QueryEscape(client.ContextName)),
						Info:       readyStr,
						Status:     "Running",
						Matches:    []MatchInfo{{Field: "Name", Value: ss.Name}},
					}
				}
			}
		}(client)
	}
	internalWg.Wait()
}

// searchConfigMaps performs regex search on configmaps
func searchConfigMaps(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var internalWg sync.WaitGroup
	for _, client := range clients {
		internalWg.Add(1)
		go func(client KubeClient) {
			defer internalWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			cmList, err := client.Clientset.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{})
			cancel()
			if err != nil {
				return
			}
			for _, cm := range cmList.Items {
				if re.MatchString(cm.Name) {
					infoStr := fmt.Sprintf("%d keys", len(cm.Data))
					results <- SearchResult{
						Type:       "ConfigMap",
						Name:       cm.Name,
						Cluster:    client.ContextName,
						Namespace:  cm.Namespace,
						Matches:    []MatchInfo{{Field: "Name", Value: cm.Name}},
						URL:        fmt.Sprintf("/configmap/detail?name=%s&namespace=%s&cluster_name=%s", url.QueryEscape(cm.Name), url.QueryEscape(cm.Namespace), url.QueryEscape(client.ContextName)),
						Info:       infoStr,
					}
				}
			}
		}(client)
	}
	internalWg.Wait()
}

// searchNodes performs regex search on nodes
func searchNodes(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var internalWg sync.WaitGroup
	for _, client := range clients {
		internalWg.Add(1)
		go func(client KubeClient) {
			defer internalWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			nodeList, err := client.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			cancel()
			if err != nil {
				return
			}
			for _, node := range nodeList.Items {
				if re.MatchString(node.Name) {
					status, _ := getNodeStatus(node)
					results <- SearchResult{
						Type:       "Node",
						Name:       node.Name,
						Cluster:    client.ContextName,
						URL:        fmt.Sprintf("/node/detail?name=%s&cluster_name=%s", url.QueryEscape(node.Name), url.QueryEscape(client.ContextName)),
						Status:     status,
						Info:       node.Status.NodeInfo.KubeletVersion,
						Matches:    []MatchInfo{{Field: "Name", Value: node.Name}},
					}
				}
			}
		}(client)
	}
	internalWg.Wait()
}

// searchPersistentVolumes performs regex search on PVs
func searchPersistentVolumes(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var internalWg sync.WaitGroup
	for _, client := range clients {
		internalWg.Add(1)
		go func(client KubeClient) {
			defer internalWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			pvList, err := client.Clientset.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
			cancel()
			if err != nil {
				return
			}
			for _, pv := range pvList.Items {
				if re.MatchString(pv.Name) {
					storage := "N/A"
					if cap, ok := pv.Spec.Capacity[v1.ResourceStorage]; ok {
						storage = formatMemory(&cap)
					}
					results <- SearchResult{
						Type:       "PersistentVolume",
						Name:       pv.Name,
						Cluster:    client.ContextName,
						Namespace:  pv.Spec.ClaimRef.Namespace,
						Matches:    []MatchInfo{{Field: "Name", Value: pv.Name}},
						URL:        "#",
						Info:       storage,
						Status:     string(pv.Status.Phase),
					}
				}
			}
		}(client)
	}
	internalWg.Wait()
}

// searchServices performs regex search on services
func searchServices(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var internalWg sync.WaitGroup
	for _, client := range clients {
		internalWg.Add(1)
		go func(client KubeClient) {
			defer internalWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			// Services are in CoreV1
			svcList, err := client.Clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{})
			cancel()
			if err != nil {
				return
			}
			for _, svc := range svcList.Items {
				if re.MatchString(svc.Name) {
					results <- SearchResult{
						Type:       "Service",
						Name:       svc.Name,
						Cluster:    client.ContextName,
						Namespace:  svc.Namespace,
						Matches:    []MatchInfo{{Field: "Name", Value: svc.Name}},
						URL:        fmt.Sprintf("/search?q=%s", url.QueryEscape(svc.Name)), // No detail page yet
						Info:       svc.Spec.ClusterIP,
						Status:     string(svc.Spec.Type),
					}
				}
			}
		}(client)
	}
	internalWg.Wait()
}

func getHeatLevel(count int) string {
	if count == 0 {
		return "heat-0"
	}
	if count <= 5 {
		return "heat-1" // Low
	}
	if count <= 20 {
		return "heat-2" // Medium
	}
	if count <= 50 {
		return "heat-3" // High
	}
	return "heat-4" // Critical
}

// searchPVCs performs regex search on PersistentVolumeClaims
func searchPVCs(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var internalWg sync.WaitGroup
	for _, client := range clients {
		internalWg.Add(1)
		go func(client KubeClient) {
			defer internalWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			pvcList, err := client.Clientset.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
			cancel()
			if err != nil {
				return
			}
			for _, pvc := range pvcList.Items {
				if re.MatchString(pvc.Name) {
					// Get storage capacity
					storage := "N/A"
					if cap, ok := pvc.Status.Capacity[v1.ResourceStorage]; ok {
						storage = formatMemory(&cap)
					}

					results <- SearchResult{
						Type:       "PVC",
						Name:       pvc.Name,
						Cluster:    client.ContextName,
						Namespace:  pvc.Namespace,
						Matches:    []MatchInfo{{Field: "Name", Value: pvc.Name}},
						URL:        fmt.Sprintf("/search?q=%s", url.QueryEscape(pvc.Name)), // Link to search for now
						Info:       storage,
						Status:     string(pvc.Status.Phase),
					}
				}
			}
		}(client)
	}
	internalWg.Wait()
}

// searchServiceAccounts performs regex search on ServiceAccounts
func searchServiceAccounts(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var internalWg sync.WaitGroup
	for _, client := range clients {
		internalWg.Add(1)
		go func(client KubeClient) {
			defer internalWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			// ServiceAccounts are in CoreV1
			saList, err := client.Clientset.CoreV1().ServiceAccounts("").List(ctx, metav1.ListOptions{})
			cancel()
			if err != nil {
				return
			}
			for _, sa := range saList.Items {
				if re.MatchString(sa.Name) {
					results <- SearchResult{
						Type:       "ServiceAccount",
						Name:       sa.Name,
						Cluster:    client.ContextName,
						Namespace:  sa.Namespace,
						Matches:    []MatchInfo{{Field: "Name", Value: sa.Name}},
						URL:        fmt.Sprintf("/serviceaccount/detail?name=%s&namespace=%s&cluster_name=%s", url.QueryEscape(sa.Name), url.QueryEscape(sa.Namespace), url.QueryEscape(client.ContextName)),
						Status:     "Active",
						Info:       fmt.Sprintf("%d Secrets", len(sa.Secrets)),
					}
				}
			}
		}(client)
	}
	internalWg.Wait()
}

// searchIngresses performs regex search on Ingresses
func searchIngresses(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var internalWg sync.WaitGroup
	for _, client := range clients {
		internalWg.Add(1)
		go func(client KubeClient) {
			defer internalWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			list, err := client.Clientset.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
			cancel()
			if err != nil { return }
			
			for _, item := range list.Items {
				if re.MatchString(item.Name) {
					var hosts []string
					for _, rule := range item.Spec.Rules {
						if rule.Host != "" { hosts = append(hosts, rule.Host) }
					}
					
					results <- SearchResult{
						Type:       "Ingress",
						Name:       item.Name,
						Cluster:    client.ContextName,
						Namespace:  item.Namespace,
						Matches:    []MatchInfo{{Field: "Name", Value: item.Name}},
						URL:        fmt.Sprintf("/ingress/detail?name=%s&namespace=%s&cluster_name=%s", url.QueryEscape(item.Name), url.QueryEscape(item.Namespace), url.QueryEscape(client.ContextName)),
						Info:       fmt.Sprintf("%d Rules", len(item.Spec.Rules)),
						Status:     "Active",
					}
				}
			}
		}(client)
	}
	internalWg.Wait()
}

// searchSecrets performs regex search on Secrets
func searchSecrets(re *regexp.Regexp, clients []KubeClient, results chan<- SearchResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var internalWg sync.WaitGroup
	for _, client := range clients {
		internalWg.Add(1)
		go func(client KubeClient) {
			defer internalWg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			// Secrets are in CoreV1
			list, err := client.Clientset.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
			cancel()
			if err != nil {
				return
			}
			for _, item := range list.Items {
				if re.MatchString(item.Name) {
					results <- SearchResult{
						Type:       "Secret",
						Name:       item.Name,
						Cluster:    client.ContextName,
						Namespace:  item.Namespace,
						Matches:    []MatchInfo{{Field: "Name", Value: item.Name}},
						URL:        fmt.Sprintf("/secret/detail?name=%s&namespace=%s&cluster_name=%s", url.QueryEscape(item.Name), url.QueryEscape(item.Namespace), url.QueryEscape(client.ContextName)),
						Info:       string(item.Type),
						Status:     "Secure",
					}
				}
			}
		}(client)
	}
	internalWg.Wait()
}

// parseContainer extracts full technical info from a v1.Container
// Used by Detail Views to populate ContainerInfo structs
func parseContainer(c v1.Container) ContainerInfo {
	info := ContainerInfo{
		Name:    c.Name,
		Image:   c.Image,
		State:   "Unknown",
		Command: c.Command,
		Env:     make(map[string]string),
		Mounts:  make(map[string]string),
	}

	// Parse Ports
	for _, p := range c.Ports {
		info.Ports = append(info.Ports, fmt.Sprintf("%s:%d/%s", p.Name, p.ContainerPort, p.Protocol))
	}

	// Parse Env Vars
	for _, e := range c.Env {
		if e.Value != "" {
			info.Env[e.Name] = e.Value
		} else if e.ValueFrom != nil {
			if e.ValueFrom.SecretKeyRef != nil {
				info.Env[e.Name] = "from(secret)"
			} else if e.ValueFrom.ConfigMapKeyRef != nil {
				info.Env[e.Name] = "from(configmap)"
			} else if e.ValueFrom.FieldRef != nil {
				info.Env[e.Name] = fmt.Sprintf("from(field:%s)", e.ValueFrom.FieldRef.FieldPath)
			} else {
				info.Env[e.Name] = "from(reference)"
			}
		}
	}

	// Parse Mounts
	for _, m := range c.VolumeMounts {
		info.Mounts[m.MountPath] = m.Name
	}

	// Parse Resources
	req := c.Resources.Requests
	lim := c.Resources.Limits
	if len(req) > 0 || len(lim) > 0 {
		info.Resources = fmt.Sprintf("CPU: %s/%s | Mem: %s/%s",
			req.Cpu().String(), lim.Cpu().String(),
			req.Memory().String(), lim.Memory().String())
	}

	return info
}