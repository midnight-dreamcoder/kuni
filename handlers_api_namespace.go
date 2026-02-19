package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Local structs for API response to ensure JSON compatibility
type nsDeploymentInfo struct {
	Cluster           string
	Name              string
	Ready             string
	Age               string
	Images            []string
	CreationTimestamp time.Time
}

type nsDaemonSetInfo struct {
	Cluster           string
	Name              string
	Ready             string
	Age               string
	CreationTimestamp time.Time
}

type nsStatefulSetInfo struct {
	Cluster           string
	Name              string
	Ready             string
	Age               string
	CreationTimestamp time.Time
}

type nsReplicaSetInfo struct {
	Cluster           string
	Name              string
	Ready             string
	Age               string
	CreationTimestamp time.Time
}

type nsPodInfo struct {
	Cluster           string
	Name              string
	Namespace         string
	Ready             string
	Status            string
	Restarts          int
	Node              string
	PodIP             string
	Age               string
	CreationTimestamp time.Time
}

type nsServiceInfo struct {
	Cluster           string
	Name              string
	Type              string
	ClusterIP         string
	Age               string
	CreationTimestamp time.Time
}

type nsIngressInfo struct {
	Cluster           string
	Name              string
	Hosts             string
	Age               string
	CreationTimestamp time.Time
}

type nsConfigInfo struct {
	Cluster           string
	Name              string
	Details           string // "X keys" or Type
	Age               string
	Kind              string // "CM" or "SEC"
	CreationTimestamp time.Time
}

type NamespaceAPIResponse struct {
	GlobalStats struct {
		TotalDeployments  int
		TotalPods         int
		TotalDaemonSets   int
		TotalStatefulSets int
		TotalServices     int
		TotalIngresses    int
		TotalConfigMaps   int
		TotalSecrets      int
		PodStatus         map[string]int
	}
	Deployments  []nsDeploymentInfo
	DaemonSets   []nsDaemonSetInfo
	StatefulSets []nsStatefulSetInfo
	ReplicaSets  []nsReplicaSetInfo
	Pods         []nsPodInfo
	Services     []nsServiceInfo
	Ingresses    []nsIngressInfo
	Configs      []nsConfigInfo // Combined CMs and Secrets for simpler table update
}

// handleGetNamespaceDetailAPI returns JSON data for the namespace detail view
func handleGetNamespaceDetailAPI(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		nsName := c.QueryParam("name")
		if nsName == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Namespace name required"})
		}

		configsToProcess, err := getConfigsToProcess(c, pattern)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}

		clients, _ := createClients(configsToProcess)

		var resp NamespaceAPIResponse
		resp.GlobalStats.PodStatus = make(map[string]int)
		resp.Deployments = []nsDeploymentInfo{}
		resp.DaemonSets = []nsDaemonSetInfo{}
		resp.StatefulSets = []nsStatefulSetInfo{}
		resp.ReplicaSets = []nsReplicaSetInfo{}
		resp.Pods = []nsPodInfo{}
		resp.Services = []nsServiceInfo{}
		resp.Ingresses = []nsIngressInfo{}
		resp.Configs = []nsConfigInfo{}

		var mutex sync.Mutex
		var wg sync.WaitGroup

		fetch := func(client KubeClient) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// Fetch all resources in parallel or sequence per cluster
			deps, _ := client.Clientset.AppsV1().Deployments(nsName).List(ctx, metav1.ListOptions{})
			dss, _ := client.Clientset.AppsV1().DaemonSets(nsName).List(ctx, metav1.ListOptions{})
			sss, _ := client.Clientset.AppsV1().StatefulSets(nsName).List(ctx, metav1.ListOptions{})
			rss, _ := client.Clientset.AppsV1().ReplicaSets(nsName).List(ctx, metav1.ListOptions{})
			pods, _ := client.Clientset.CoreV1().Pods(nsName).List(ctx, metav1.ListOptions{})
			svcs, _ := client.Clientset.CoreV1().Services(nsName).List(ctx, metav1.ListOptions{})
			ings, _ := client.Clientset.NetworkingV1().Ingresses(nsName).List(ctx, metav1.ListOptions{})
			cms, _ := client.Clientset.CoreV1().ConfigMaps(nsName).List(ctx, metav1.ListOptions{})
			secs, _ := client.Clientset.CoreV1().Secrets(nsName).List(ctx, metav1.ListOptions{})

			mutex.Lock()
			defer mutex.Unlock()

			if deps != nil {
				for _, d := range deps.Items {
					resp.GlobalStats.TotalDeployments++
					var images []string
					for _, c := range d.Spec.Template.Spec.Containers {
						images = append(images, c.Image)
					}
					resp.Deployments = append(resp.Deployments, nsDeploymentInfo{
						Cluster: client.ContextName, Name: d.Name, Ready: fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, *d.Spec.Replicas), Age: formatAge(d.CreationTimestamp), Images: images, CreationTimestamp: d.CreationTimestamp.Time,
					})
				}
			}
			if dss != nil {
				for _, d := range dss.Items {
					resp.GlobalStats.TotalDaemonSets++
					resp.DaemonSets = append(resp.DaemonSets, nsDaemonSetInfo{
						Cluster: client.ContextName, Name: d.Name, Ready: fmt.Sprintf("%d/%d", d.Status.NumberReady, d.Status.DesiredNumberScheduled), Age: formatAge(d.CreationTimestamp), CreationTimestamp: d.CreationTimestamp.Time,
					})
				}
			}
			if sss != nil {
				for _, s := range sss.Items {
					resp.GlobalStats.TotalStatefulSets++
					var replicas int32 = 1
					if s.Spec.Replicas != nil {
						replicas = *s.Spec.Replicas
					}
					resp.StatefulSets = append(resp.StatefulSets, nsStatefulSetInfo{
						Cluster: client.ContextName, Name: s.Name, Ready: fmt.Sprintf("%d/%d", s.Status.ReadyReplicas, replicas), Age: formatAge(s.CreationTimestamp), CreationTimestamp: s.CreationTimestamp.Time,
					})
				}
			}
			if rss != nil {
				for _, r := range rss.Items {
					var replicas int32 = 1
					if r.Spec.Replicas != nil {
						replicas = *r.Spec.Replicas
					}
					resp.ReplicaSets = append(resp.ReplicaSets, nsReplicaSetInfo{
						Cluster: client.ContextName, Name: r.Name, Ready: fmt.Sprintf("%d/%d", r.Status.ReadyReplicas, replicas), Age: formatAge(r.CreationTimestamp), CreationTimestamp: r.CreationTimestamp.Time,
					})
				}
			}
			if pods != nil {
				for _, p := range pods.Items {
					resp.GlobalStats.TotalPods++
					readyCount := 0
					restartCount := 0
					displayStatus := getPodDisplayStatus(p)
					resp.GlobalStats.PodStatus[displayStatus]++
					for _, cs := range p.Status.ContainerStatuses {
						if cs.Ready {
							readyCount++
						}
						restartCount += int(cs.RestartCount)
					}
					resp.Pods = append(resp.Pods, nsPodInfo{
						Cluster: client.ContextName, Name: p.Name, Namespace: p.Namespace, Ready: fmt.Sprintf("%d/%d", readyCount, len(p.Spec.Containers)), Status: displayStatus, Restarts: restartCount, Node: p.Spec.NodeName, PodIP: p.Status.PodIP, Age: formatAge(p.CreationTimestamp), CreationTimestamp: p.CreationTimestamp.Time,
					})
				}
			}
			if svcs != nil {
				for _, s := range svcs.Items {
					resp.GlobalStats.TotalServices++
					resp.Services = append(resp.Services, nsServiceInfo{
						Cluster: client.ContextName, Name: s.Name, Type: string(s.Spec.Type), ClusterIP: s.Spec.ClusterIP, Age: formatAge(s.CreationTimestamp), CreationTimestamp: s.CreationTimestamp.Time,
					})
				}
			}
			if ings != nil {
				for _, i := range ings.Items {
					resp.GlobalStats.TotalIngresses++
					var hosts []string
					for _, rule := range i.Spec.Rules {
						hosts = append(hosts, rule.Host)
					}
					hostsStr := ""
					if len(hosts) > 0 {
						hostsStr = hosts[0]
					}
					resp.Ingresses = append(resp.Ingresses, nsIngressInfo{
						Cluster: client.ContextName, Name: i.Name, Hosts: hostsStr, Age: formatAge(i.CreationTimestamp), CreationTimestamp: i.CreationTimestamp.Time,
					})
				}
			}
			if cms != nil {
				for _, c := range cms.Items {
					resp.GlobalStats.TotalConfigMaps++
					resp.Configs = append(resp.Configs, nsConfigInfo{Cluster: client.ContextName, Name: c.Name, Details: fmt.Sprintf("%d keys", len(c.Data)), Age: formatAge(c.CreationTimestamp), Kind: "CM", CreationTimestamp: c.CreationTimestamp.Time})
				}
			}
			if secs != nil {
				for _, s := range secs.Items {
					resp.GlobalStats.TotalSecrets++
					resp.Configs = append(resp.Configs, nsConfigInfo{Cluster: client.ContextName, Name: s.Name, Details: string(s.Type), Age: formatAge(s.CreationTimestamp), Kind: "SEC", CreationTimestamp: s.CreationTimestamp.Time})
				}
			}
		}

		for _, client := range clients {
			wg.Add(1)
			go fetch(client)
		}
		wg.Wait()

		// Sort all lists by CreationTimestamp Descending (Newest First)
		sort.Slice(resp.Deployments, func(i, j int) bool {
			return resp.Deployments[i].CreationTimestamp.After(resp.Deployments[j].CreationTimestamp)
		})
		sort.Slice(resp.DaemonSets, func(i, j int) bool {
			return resp.DaemonSets[i].CreationTimestamp.After(resp.DaemonSets[j].CreationTimestamp)
		})
		sort.Slice(resp.StatefulSets, func(i, j int) bool {
			return resp.StatefulSets[i].CreationTimestamp.After(resp.StatefulSets[j].CreationTimestamp)
		})
		sort.Slice(resp.ReplicaSets, func(i, j int) bool {
			return resp.ReplicaSets[i].CreationTimestamp.After(resp.ReplicaSets[j].CreationTimestamp)
		})
		sort.Slice(resp.Pods, func(i, j int) bool { return resp.Pods[i].CreationTimestamp.After(resp.Pods[j].CreationTimestamp) })
		sort.Slice(resp.Services, func(i, j int) bool {
			return resp.Services[i].CreationTimestamp.After(resp.Services[j].CreationTimestamp)
		})
		sort.Slice(resp.Ingresses, func(i, j int) bool {
			return resp.Ingresses[i].CreationTimestamp.After(resp.Ingresses[j].CreationTimestamp)
		})
		sort.Slice(resp.Configs, func(i, j int) bool { return resp.Configs[i].CreationTimestamp.After(resp.Configs[j].CreationTimestamp) })

		return c.JSON(http.StatusOK, resp)
	}
}
