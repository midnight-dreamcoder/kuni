package main

import (
	"k8s.io/client-go/kubernetes"
	"time"
)

// KubeClient holds a ready-to-use clientset and its context name
type KubeClient struct {
	Clientset   *kubernetes.Clientset
	ContextName string
	ConfigPath  string
}

// ClusterInfo holds the status of a single cluster
type ClusterInfo struct {
	Name       string
	ApiServer  string
	Status     string
	Version    string
	NodeCount  int
	ConfigName string
	Latency    string
	Provider   string
}

// PageBase holds all the common data for every template
type PageBase struct {
	Title                string
	ActivePage           string
	SelectedClusterCount int
	QueryString          string
	CacheBuster          string
	ErrorLogs            []string
	SuccessLogs          []string
	LastRefreshed        string
	IsSearchPage         bool
	IsAdmin              bool   // If false, UI hides write actions
	UserName             string // e.g. "admin" or "guest"
}

// ClusterPageData is the main data object for the clusters.html template
type ClusterPageData struct {
	PageBase
	Clusters         []ClusterInfo
	SelectedClusters map[string]bool
}

// NodeInfo holds the data for one node
type NodeInfo struct {
	Cluster     string
	Name        string
	Status      string
	Reason      string
	Role        string
	Schedulable string
	Kubelet     string
	Runtime     string
	CpuAlloc    string
	MemAlloc    string
	Taints      int
}

// NodePageData is the main data object for the nodes.html template
type NodePageData struct {
	PageBase
	Nodes        []NodeInfo
	TotalNodes   int
	ClusterStats []ClusterStat
	StatusStats  []PodStatusStat
	SchedStats   []PodStatusStat
}

// AggregatedNamespaceView holds the "pivoted" data
type AggregatedNamespaceView struct {
	Name         string
	Clusters     []string
	StatusCounts map[string]int
}

// ClusterStat holds a simple count for the stats box
type ClusterStat struct {
	Name  string
	Count int
}

// NamespacePageData is the main object for the all-namespaces.html template
type NamespacePageData struct {
	PageBase
	Namespaces              []AggregatedNamespaceView
	TotalUniqueNamespaces   int
	ClusterStats            []ClusterStat
	NamespaceStatusStats    []PodStatusStat
	TotalNamespaceInstances int
}

// AggregatedDeploymentView holds the "pivoted" data for a single deployment name
type AggregatedDeploymentView struct {
	Name                 string
	TotalReadyReplicas   int
	TotalDesiredReplicas int
	Clusters             []string
	Namespaces           []string
	Images               []string
	Strategies           []string
}

// NamespaceStat holds a simple count for the stats box
type NamespaceStat struct {
	Name        string
	Count       int
	Color       string
	ErrorDetail string
}

// DeploymentPageData is the main data object for the deployments.html template
type DeploymentPageData struct {
	PageBase
	Deployments            []AggregatedDeploymentView
	TotalUniqueDeployments int
	ClusterStats           []ClusterStat
	NamespaceStats         []NamespaceStat
	NamespaceBarStats      []NamespaceStat
}

// PodInfo holds the data for one pod
type PodInfo struct {
	Cluster   string
	Namespace string
	Name      string
	Ready     string
	Status    string
	Reason    string
	Restarts  int
	Node      string
	PodIP     string
	QoS       string
	Age       string
}

// PodPageData is the main data object for the pods.html template
type PodPageData struct {
	PageBase
	Pods           []PodInfo
	TotalPods      int
	ClusterStats   []ClusterStat
	NamespaceStats []NamespaceStat
	PodStatusStats []PodStatusStat
	PodReasonStats []ReasonStat
}

// MatchInfo holds a single field/value match
type MatchInfo struct {
	Field string
	Value string
}

// SearchResult holds a single matched item
type SearchResult struct {
	Type      string
	Name      string
	Cluster   string
	Namespace string
	URL       string
	Status    string
	Info      string
	Node      string
	Matches   []MatchInfo
}

// SearchPageData is the main data object for the search.html template
type SearchPageData struct {
	PageBase
	Query   string
	Results []SearchResult
	Error   string
}

// DeploymentDetailView holds the "overview" data for a deployment
type DeploymentDetailView struct {
	Status       string
	Strategy     string
	Selector     string
	Images       []string
	Conditions   []string
	MinReadySecs int32

	RolloutStatus  RolloutStatusInfo
	RolloutHistory []RolloutHistoryInfo
}

// DeploymentDetailPageData is the main data object for the detail page
type DeploymentDetailPageData struct {
	PageBase
	DeploymentName string
	Overviews      map[string]map[string]DeploymentDetailView
	Pods           map[string]map[string][]PodInfo
	ClusterNames   []string
	NamespaceNames map[string][]string
}

// ReplicaSetInfo holds the data for one replicaset
type ReplicaSetInfo struct {
	Cluster   string
	Namespace string
	Name      string
	Ready     string
	Owner     string
	Age       string
}

// ReplicaSetPageData is the main data object for the replicasets.html template
type ReplicaSetPageData struct {
	PageBase
	ReplicaSets      []ReplicaSetInfo
	TotalReplicaSets int
	ClusterStats     []ClusterStat
	NamespaceStats   []NamespaceStat
	OwnerStats       []NamespaceStat
}

// ReplicaSetDetailPageData is the main data object for the replicaset-detail.html template
type ReplicaSetDetailPageData struct {
	PageBase
	ClusterName    string
	NamespaceName  string
	ReplicaSetName string
	Status         string
	Selector       string
	OwnerName      string
	OwnerKind      string
	Age            string
	Pods           []PodInfo
	Events         []EventInfo
}

// SimpleDeploymentInfo is for the namespace detail list.
type SimpleDeploymentInfo struct {
	Name     string
	Ready    string
	Images   []string
	Strategy string
	Age      string
}

// NamespaceDetailView holds all resources for one namespace on one cluster
type NamespaceDetailView struct {
	TotalPods        int
	TotalDeployments int
	TotalReplicaSets int
	Pods             []PodInfo
	Deployments      []SimpleDeploymentInfo
	ReplicaSets      []ReplicaSetInfo
}

// NamespaceDetailPageData is the main data object for the detail page
type NamespaceDetailPageData struct {
	PageBase
	NamespaceName string
	ClusterNames  []string
	Details       map[string]NamespaceDetailView
}

// ContainerInfo holds data for a single container in a pod
type ContainerInfo struct {
	Name         string
	Image        string
	State        string
	Reason       string
	Ready        bool
	RestartCount int
	Ports        []string
	Env          map[string]string
	Mounts       map[string]string
	Resources    string
	Command      []string
}

// PodConditionInfo holds data for a pod's condition
type PodConditionInfo struct {
	Type          string
	Status        string
	Reason        string
	Message       string
	LastHeartbeat string // Used in NodeConditionInfo
}

// VolumeInfo holds data for a pod's volume
type VolumeInfo struct {
	Name string
	Type string
}

// EventInfo holds data for a pod event
type EventInfo struct {
	Cluster   string
	Namespace string
	Object    string // e.g., "pod/my-pod-123"
	Type      string
	Reason    string
	Message   string
	Count     int
	LastSeen  string
	Timestamp time.Time // For sorting
}

// EventBucket holds aggregated counts for a time slice
type EventBucket struct {
	Label        string // e.g., "0-4 min ago"
	WarningCount int
	NormalCount  int
}

// PodDetailPageData is the main data object for the pod-detail.html template
type PodDetailPageData struct {
	PageBase
	ClusterName    string
	NamespaceName  string
	PodName        string
	Status         string
	Reason         string
	Node           string
	PodIP          string
	QoS            string
	ServiceAccount string
	Age            string
	OwnerName      string
	OwnerKind      string
	Owner          string
	Labels         map[string]string
	Annotations    map[string]string
	NodeSelector   map[string]string
	Tolerations    []string
	InitContainers []ContainerInfo
	Containers     []ContainerInfo
	Conditions     []PodConditionInfo
	Volumes        []VolumeInfo
	Events         []EventInfo
}

// PodStatusStat holds data for the pod status chart
type PodStatusStat struct {
	Status string
	Count  int
}

// ReasonStat holds data for the pod reason chart
type ReasonStat struct {
	Reason string
	Count  int
}

// WorkloadStat holds simple ready/not-ready counts
type WorkloadStat struct {
	Ready    int
	NotReady int
	Total    int
}

// RestartingPodInfo holds data for the "top restarts" list
type RestartingPodInfo struct {
	Cluster   string
	Namespace string
	Name      string
	Restarts  int
	Reason    string
}

// WorkloadOverviewPageData is the main data object for the page
type WorkloadOverviewPageData struct {
	PageBase
	PodStatus         []PodStatusStat
	PodStatusTotal    int
	DeploymentStatus  WorkloadStat
	ReplicaSetStatus  WorkloadStat
	RecentWarnings    []EventInfo
	RecentRestarts    []RestartingPodInfo
	PodReasonStats    []ReasonStat
	PodNamespaceStats []NamespaceStat
}

// DaemonSetInfo holds the data for one daemonset
type DaemonSetInfo struct {
	Cluster   string
	Namespace string
	Name      string
	Ready     string
	Node      string
	Age       string
}

// DaemonSetDetailView holds the "overview" data for a daemonset
type DaemonSetDetailView struct {
	Status       string
	Selector     string
	NodeSelector string
	Age          string
}

// DaemonSetDetailPageData is the main data object for the daemonset-detail.html template
type DaemonSetDetailPageData struct {
	PageBase
	ClusterName   string
	NamespaceName string
	DaemonSetName string
	Overview      DaemonSetDetailView
	Pods          []PodInfo
	Events        []EventInfo
}

// DaemonSetPageData is the main data object for the daemonsets.html template
type DaemonSetPageData struct {
	PageBase
	DaemonSets        []DaemonSetInfo
	TotalDaemonSets   int
	ClusterStats      []ClusterStat
	NamespaceStats    []NamespaceStat
	NodeSelectorStats []NamespaceStat
}

// StatefulSetInfo holds the data for one statefulset
type StatefulSetInfo struct {
	Cluster   string
	Namespace string
	Name      string
	Ready     string
	Age       string
}

// StatefulSetDetailView holds the "overview" data for a statefulset
type StatefulSetDetailView struct {
	Status      string
	Selector    string
	ServiceName string
	Age         string
}

// StatefulSetDetailPageData is the main data object for the statefulset-detail.html template
type StatefulSetDetailPageData struct {
	PageBase
	ClusterName     string
	NamespaceName   string
	StatefulSetName string
	Overview        StatefulSetDetailView
	Pods            []PodInfo
	Events          []EventInfo
}

// StatefulSetPageData is the main data object for the statefulsets.html template
type StatefulSetPageData struct {
	PageBase
	StatefulSets      []StatefulSetInfo
	TotalStatefulSets int
	ClusterStats      []ClusterStat
	NamespaceStats    []NamespaceStat
}

// ConfigMapInfo holds the data for one configmap in a list
type ConfigMapInfo struct {
	Cluster   string
	Namespace string
	Name      string
	DataKeys  int
	Age       string
}

// ConfigMapPageData is the main data object for the configmaps.html template
type ConfigMapPageData struct {
	PageBase
	ConfigMaps      []ConfigMapInfo
	TotalConfigMaps int
	ClusterStats    []ClusterStat
	NamespaceStats  []NamespaceStat
}

// ConfigMapDetailPageData is the main data object for the configmap-detail.html template
type ConfigMapDetailPageData struct {
	PageBase
	ClusterName   string
	NamespaceName string
	ConfigMapName string
	Age           string
	Data          map[string]string
	Events        []EventInfo
}

// NodeAddressInfo holds a node's address
type NodeAddressInfo struct {
	Type    string
	Address string
}

// NodeTaintInfo holds a node's taint
type NodeTaintInfo struct {
	Key    string
	Value  string
	Effect string
}

// ResourceSummary holds aggregated cluster resource data
type ResourceSummary struct {
	CapacityCpu    string // "128"
	AllocatableCpu string // "120"
	UsageCpu       string // "8.5"
	
	CapacityMem    string // "512 Gi"
	AllocatableMem string // "480 Gi"
	UsageMem       string // "100 Gi"
	
	// Percentages (of Capacity)
	AllocatableCpuPercent float64
	UsageCpuPercent       float64
	
	AllocatableMemPercent float64
	UsageMemPercent       float64
}

// ClusterDetailPageData is the main data object for the cluster-detail.html template
type ClusterDetailPageData struct {
	PageBase
	Cluster    ClusterInfo
	Nodes      []NodeInfo
	Events     []EventInfo
	Namespaces []string
	Resources  ResourceSummary
}

// NodeDetailPageData is the main data object for the node-detail.html template
type NodeDetailPageData struct {
	PageBase
	ClusterName      string
	NodeName         string
	KubeletVersion   string
	OS               string
	ContainerRuntime string
	ProviderID       string
	Age              string
	Role             string
	Addresses        []NodeAddressInfo
	Taints           []NodeTaintInfo
	Labels           map[string]string
	Annotations      map[string]string
	Conditions       []PodConditionInfo
	Capacity         map[string]string
	Allocatable      map[string]string
	Resources        ResourceSummary
	Pods             []PodInfo
	Events           []EventInfo
}

// ClusterOverviewPageData is the main data object for the overview.html template
type ClusterOverviewPageData struct {
	PageBase
	TotalClusters    int
	NodeStatus       WorkloadStat
	PVStatus         []PodStatusStat
	TotalPVs         int
	ClusterResources map[string]ResourceSummary 
	ClusterNames     []string
}

// EventPageData is the main data object for the events.html template
type EventPageData struct {
	PageBase
	RecentEvents   []EventInfo
	TotalEvents    int
	ClusterStats   []ClusterStat
	NamespaceStats []NamespaceStat
	ReasonStats    []ReasonStat
	HeatmapNamespaces []string
	HeatmapRows       []HeatmapRow
}

// ServiceInfo holds the data for one service in a list
type ServiceInfo struct {
	Cluster    string
	Namespace  string
	Name       string
	Type       string
	ClusterIP  string
	ExternalIP string
	Age        string
}

// ServicePageData is the main data object for the services.html template
type ServicePageData struct {
	PageBase
	Services      []ServiceInfo
	TotalServices int
	ClusterStats  []ClusterStat
	NamespaceStats []NamespaceStat
}

// HeatmapCell holds a single data point for the heatmap
type HeatmapCell struct {
	Count          int
	Level          string
	TopReason      string
	TopReasonCount int
}

// HeatmapRow holds all the cells for a single cluster
type HeatmapRow struct {
	ClusterName string
	Cells       []HeatmapCell
}

// PVCInfo holds the data for one PersistentVolumeClaim
type PVCInfo struct {
	Cluster      string
	Namespace    string
	Name         string
	Status       string // e.g., Bound, Pending
	VolumeName   string
	Capacity     string
	StorageClass string
	Age          string
}

// PVCPageData is the main data object for the pvcs.html template
type PVCPageData struct {
	PageBase
	PVCs           []PVCInfo
	TotalPVCs      int
	ClusterStats   []ClusterStat
	NamespaceStats []NamespaceStat
	StatusStats    []PodStatusStat
}

// ServiceAccountInfo holds the data for one SA in a list
type ServiceAccountInfo struct {
	Cluster   string
	Namespace string
	Name      string
	Secrets   int
	Age       string
}

// ServiceAccountPageData is the main data object for the serviceaccounts.html template
type ServiceAccountPageData struct {
	PageBase
	ServiceAccounts      []ServiceAccountInfo
	TotalServiceAccounts int
	ClusterStats         []ClusterStat
	NamespaceStats       []NamespaceStat
}

// ServiceAccountDetailPageData is the main data object for the serviceaccount-detail.html template
type ServiceAccountDetailPageData struct {
	PageBase
	ClusterName        string
	NamespaceName      string
	ServiceAccountName string
	Age                string
	Secrets            []string 
	ImagePullSecrets   []string
	Pods               []PodInfo
	Events             []EventInfo
}

// ServicePortInfo holds data for a single port
type ServicePortInfo struct {
	Name       string
	Port       int32
	TargetPort string
	Protocol   string
	NodePort   int32
}

// EndpointInfo holds data for a single backing pod/ip
type EndpointInfo struct {
	IP        string
	NodeName  string
	TargetRef string // e.g. "pod/my-pod-123"
	Ready     bool
}

// ServiceDetailPageData is the main data object for the service-detail.html template
type ServiceDetailPageData struct {
	PageBase
	ClusterName     string
	NamespaceName   string
	ServiceName     string
	Type            string
	ClusterIP       string
	ExternalIPs     []string
	LoadBalancerIP  string
	Selector        map[string]string
	Age             string
	SessionAffinity string
	Ports           []ServicePortInfo
	Endpoints       []EndpointInfo
	Events          []EventInfo
}

// PVCDetailPageData is the main data object for the pvc-detail.html template
type PVCDetailPageData struct {
	PageBase
	ClusterName   string
	NamespaceName string
	PVCName       string
	Status        string
	Volume        string
	Capacity      string
	StorageClass  string
	AccessModes   []string
	Age           string
	MountedBy     []PodInfo
	Events        []EventInfo
}

// IngressInfo holds data for the list view
type IngressInfo struct {
	Cluster   string
	Namespace string
	Name      string
	Hosts     string // Comma-separated
	Address   string // LoadBalancer IP/Hostname
	Age       string
}

// IngressPageData is the main data for ingresses.html
type IngressPageData struct {
	PageBase
	Ingresses      []IngressInfo
	TotalIngresses int
	ClusterStats   []ClusterStat
	NamespaceStats []NamespaceStat
}

// IngressRuleInfo holds data for a specific routing rule
type IngressRuleInfo struct {
	Host        string
	Path        string
	PathType    string
	ServiceName string
	ServicePort string
}

// IngressDetailPageData is the main data for ingress-detail.html
type IngressDetailPageData struct {
	PageBase
	ClusterName   string
	NamespaceName string
	IngressName   string
	ClassName     string
	Address       string
	Age           string
	Rules         []IngressRuleInfo
	TLS           []string
	Annotations   map[string]string
	Events        []EventInfo
}

// SecretInfo holds data for the list view
type SecretInfo struct {
	Cluster   string
	Namespace string
	Name      string
	Type      string
	KeyCount  int
	Age       string
}

// SecretPageData is the main data for secrets.html
type SecretPageData struct {
	PageBase
	Secrets        []SecretInfo
	TotalSecrets   int
	ClusterStats   []ClusterStat
	NamespaceStats []NamespaceStat
	TypeStats      []ReasonStat
}

// SecretDetailPageData is the main data for secret-detail.html
type SecretDetailPageData struct {
	PageBase
	ClusterName   string
	NamespaceName string
	SecretName    string
	Type          string
	Age           string
	Data          map[string]string 
	Events        []EventInfo
}

// CRDInfo holds metadata about a Custom Resource Definition
type CRDInfo struct {
	Name     string
	Group    string
	Version  string
	Kind     string
	Scope    string
	Age      string
	Clusters []string 
}

// CRDPageData is the main data for crds.html
type CRDPageData struct {
	PageBase
	CRDs       []CRDInfo
	TotalCRDs  int
	GroupStats []ClusterStat
	ScopeStats []ClusterStat
}

// RolloutStatusInfo holds the computed status (like 'kubectl rollout status')
type RolloutStatusInfo struct {
	Message    string
	IsComplete bool
}

// RolloutHistoryInfo holds one revision (like 'kubectl rollout history')
type RolloutHistoryInfo struct {
	Revision    int64
	ChangeCause string
	Age         string
	Images      []string
}