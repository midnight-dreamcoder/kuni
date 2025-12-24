package main

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"net/url"
	"os" // Added to check Env Vars
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"k8s.io/client-go/util/homedir"

	// Side-effect import for GCP Native Auth (CRITICAL for GKE integration)
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
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
	// 1. Load Config
	LoadConfig("config.json")

	InitDB(CurrentConfig.DatabasePath)
	CreateDefaultUser()
	
	// --- 2. Initialize Echo ---
	e := echo.New()
	// Disable Debug mode in production for performance
	e.Debug = true 

	// --- GLOBAL MIDDLEWARE: OPTIONAL AUTH ---
	// Allows Guests to view pages, but identifies Logged-in Users
    e.Use(OptionalAuthMiddleware)

	// --- 2.1 Serve static files ---
	// Serve the entire static folder (CSS, JS, Images)
	e.Static("/static", "views/static") 
	// Keep this for backward compatibility if you haven't moved style.css yet
	e.File("/style.css", "views/style.css") 

	// --- 2.2 Initialize the custom template renderer ---
	t := &TemplateRenderer{
		templates: template.Must(template.New("main").Funcs(template.FuncMap{
			"ToLower": strings.ToLower,
			"fdiv": func(a, b int) float64 {
				if b == 0 {
					return 0.0
				}
				return float64(a) / float64(b)
			},
			"mulf": func(a, b float64) float64 {
				return a * b
			},
			"add": func(a, b int) int {
				return a + b
			},
			"mod": func(a, b int) int {
				return a % b
			},
			"ColorSlice": func() []string {
				return []string{"#22d3ee", "#f59e0b", "#9b59b6", "#ef4444", "#3498db", "#f1c40f", "#2ecc71", "#8e44ad", "#c0392b", "#9ca3af"}
			},
			"js": func(s string) template.JS {
				return template.JS(fmt.Sprintf("%q", s))
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
			"MakeOwnerURL": func(qs string, name string, kind string, ns string, cluster string) template.HTML {
				var path string
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
					return template.HTML(fmt.Sprintf("/search?q=%s", url.QueryEscape(name)))
				}
				
				v, _ := url.ParseQuery(strings.TrimPrefix(qs, "?"))
				v.Add("name", name)
				v.Add("namespace", ns)
				v.Add("cluster_name", cluster)

				return template.HTML(path + "?" + v.Encode())
			},
		}).ParseGlob("views/*.html")),
	}

	e.Renderer = t

	// --- 3. Define the Route Handlers ---
	
	// FIX: Allow Environment Variable override for Docker
	var kubeDir string
	if envDir := os.Getenv("KUBECONFIG_DIR"); envDir != "" {
		kubeDir = envDir
		log.Printf("📂 Using Kubeconfig dir from Env: %s", kubeDir)
	} else {
		home := homedir.HomeDir()
		if home == "" {
			log.Fatal("Error: HOME environment variable not set.")
		}
		kubeDir = filepath.Join(home, ".kube")
	}

	// Ensure the directory exists (helpful for empty Docker mounts)
	if _, err := os.Stat(kubeDir); os.IsNotExist(err) {
		log.Printf("⚠️ Warning: Kubeconfig directory does not exist: %s", kubeDir)
		os.MkdirAll(kubeDir, 0755)
	}

	pattern := filepath.Join(kubeDir, "config-ops*")
	log.Printf("🔍 Server configured to search for files matching: %s\n", pattern)

	// Register routes

	// AUTH ROUTES
    e.GET("/login", handleLoginShow())
    e.POST("/login", handleLoginSubmit())
    e.GET("/logout", handleLogout())

    // USER MANAGEMENT
	e.GET("/users", handleGetUsers())
	e.POST("/users/add", handleAddUser())
	e.POST("/users/delete", handleDeleteUser())

	// CONFIG MANAGEMENT
    e.GET("/kubeconfigs", handleGetKubeConfigs(pattern))
    e.POST("/kubeconfigs/add", handleAddKubeConfig(pattern))
    e.POST("/kubeconfigs/delete", handleDeleteKubeConfig(pattern))

    // APP ROUTES
	e.GET("/", handleSearch(pattern))
	e.GET("/overview", handleGetClusterOverview(pattern))
	
	// SEARCH HANDLERS
	e.GET("/search", handleSearch(pattern))
	e.GET("/api/search", handleSearchAPI(pattern))

	e.GET("/workload", handleGetWorkloadOverview(pattern))
	e.GET("/clusters", handleGetClusters(pattern))
	e.GET("/api/cluster/status", handleGetClusterStatusAPI(pattern))
	e.GET("/cluster/detail", handleGetClusterDetail(pattern))
	e.POST("/cluster/upload", handleUploadConfig(kubeDir))
	e.GET("/cluster/discover-eks", handleDiscoverEKS(kubeDir, "us-east-2"))
	e.GET("/cluster/discover-gke", handleDiscoverGKE(kubeDir))
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
	e.GET("/flux", handleGetFlux(pattern))

	// --- 4. Start the Server using Config ---
	port := ":8080"
	if CurrentConfig != nil && CurrentConfig.ServerPort != "" {
		port = CurrentConfig.ServerPort
	}
	
	log.Printf("🚀 K8s Universal Inspector starting on http://localhost%s", port)
	e.Logger.Fatal(e.Start(port))
}