package main

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"k8s.io/client-go/util/homedir"
)

// TemplateRenderer is a custom html/template renderer for Echo
type TemplateRenderer struct {
	templates *template.Template
}

// Render renders a template document
func (t *TemplateRenderer) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.templates.ExecuteTemplate(w, name, data)
}

func main() {
	// --- 1. Initialize Echo ---
	e := echo.New()
	e.Debug = true

	// --- 1.1 Serve static files ---
	e.File("/style.css", "views/style.css")

	// --- 1.2 Initialize the custom template renderer ---
	t := &TemplateRenderer{
		templates: template.Must(template.New("main").Funcs(template.FuncMap{
			"ToLower": strings.ToLower,
			"fdiv": func(a, b int) float64 { // For workload charts
				if b == 0 {
					return 0.0
				}
				return float64(a) / float64(b)
			},
			"mulf": func(a, b float64) float64 { // For workload charts
				return a * b
			},
			"MakeURL": func(baseQuery string, path string, params ...string) (template.HTML, error) {
				v, _ := url.ParseQuery(strings.TrimPrefix(baseQuery, "?"))
				if len(params)%2 != 0 {
					return "", fmt.Errorf("invalid params for MakeURL: must be key-value pairs")
				}
				for i := 0; i < len(params); i += 2 {
					v.Add(params[i], params[i+1])
				}
				return template.HTML(path + "?" + v.Encode()), nil
			},
			
			// --- THIS IS THE FIX ---
			"add": func(a, b int) int {
				return a + b
			},
			"mod": func(a, b int) int {
				return a % b
			},
			// This helper provides the color list to the template
			"ColorSlice": func() []string {
				return []string{"#22d3ee", "#f59e0b", "#9b59b6", "#ef4444", "#3498db", "#f1c40f", "#2ecc71", "#8e44ad", "#c0392b", "#9ca3af"}
			},
			// --- END OF FIX ---
			// js safely escapes a string for JavaScript
			"js": func(s string) template.JS {
				return template.JS(fmt.Sprintf("%q", s))
			},
			// Add this function to your existing template.FuncMap in main.go
			"MakeOwnerURL": func(qs string, name string, kind string, ns string, cluster string) template.HTML {
			    var path string
			    // Determine the correct detail path based on the resource Kind
			    switch kind {
			    case "ReplicaSet":
			        path = "/replicaset/detail"
			    case "Deployment":
			        path = "/deployment/detail"
			    case "DaemonSet":
			        path = "/daemonset/detail"
			    case "StatefulSet":
			        path = "/statefulset/detail"
			    default:
			        // Fallback to search for unhandled types (like Job)
			        return template.HTML(fmt.Sprintf("/search?q=%s", url.QueryEscape(name)))
			    }
			    
			    // Construct the URL by adding necessary params (name, namespace, cluster)
			    v, _ := url.ParseQuery(strings.TrimPrefix(qs, "?"))
			    v.Add("name", name)
			    v.Add("namespace", ns)
			    v.Add("cluster_name", cluster)

			    return template.HTML(path + "?" + v.Encode())
			},


		}).ParseGlob("views/*.html")),
	}

	e.Renderer = t

	// --- 2. Define the Route Handlers ---
	home := homedir.HomeDir()
	if home == "" {
		log.Fatal("Error: HOME environment variable not set.")
	}
	kubeDir := filepath.Join(home, ".kube")
	pattern := filepath.Join(kubeDir, "config-ops*")
	log.Printf("🔍 Server configured to search for files matching: %s\n", pattern)

	// Register routes
	e.GET("/", handleSearch(pattern)) // Default to search
	e.GET("/overview", handleGetClusterOverview(pattern))
	e.GET("/search", handleSearch(pattern))
	e.GET("/workload", handleGetWorkloadOverview(pattern))
	e.GET("/clusters", handleGetClusters(pattern))
	e.GET("/cluster/detail", handleGetClusterDetail(pattern))
	e.POST("/cluster/upload", handleUploadConfig(kubeDir))
	e.GET("/cluster/discover-eks", handleDiscoverEKS(kubeDir, "us-east-2"))
	e.GET("/nodes", handleGetNodes(pattern))
	e.GET("/node/detail", handleGetNodeDetail(pattern))
	e.GET("/events", handleGetEvents(pattern))
	e.GET("/services", handleGetServices(pattern))
	e.GET("/service/detail", handleGetServiceDetail(pattern))
	e.GET("/ingresses", handleGetIngresses(pattern))
	e.GET("/ingress/detail", handleGetIngressDetail(pattern))
	e.GET("/serviceaccounts", handleGetServiceAccounts(pattern))
	e.GET("/serviceaccount/detail", handleGetServiceAccountDetail(pattern))
	e.GET("/namespaces", handleGetNamespaces(pattern))
	e.GET("/namespace/detail", handleGetNamespaceDetail(pattern))
	e.GET("/deployments", handleGetDeployments(pattern))
	e.GET("/deployment/detail", handleGetDeploymentDetail(pattern))
	e.GET("/pods", handleGetPods(pattern))
	e.GET("/pod/detail", handleGetPodDetail(pattern))
	e.GET("/pod/logs", handleGetPodLogs(pattern))
	e.GET("/replicasets", handleGetReplicaSets(pattern))
	e.GET("/replicaset/detail", handleGetReplicaSetDetail(pattern))
	e.GET("/daemonsets", handleGetDaemonSets(pattern))
	e.GET("/daemonset/detail", handleGetDaemonSetDetail(pattern))
	e.GET("/statefulsets", handleGetStatefulSets(pattern))
	e.GET("/statefulset/detail", handleGetStatefulSetDetail(pattern))
	e.GET("/configmaps", handleGetConfigMaps(pattern))
	e.GET("/configmap/detail", handleGetConfigMapDetail(pattern))
	e.GET("/pvcs", handleGetPVCs(pattern))
	e.GET("/pvc/detail", handleGetPVCDetail(pattern))
	e.GET("/crds", handleGetCRDs(pattern))
	e.GET("/secrets", handleGetSecrets(pattern))
	e.GET("/secret/detail", handleGetSecretDetail(pattern))


	// --- 3. Start the Server ---
	log.Println("🚀 K8s Universal Inspector starting on http://localhost:8080")
	e.Logger.Fatal(e.Start(":8080"))
}