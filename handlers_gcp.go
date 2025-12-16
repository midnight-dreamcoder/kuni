package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"path/filepath"

	container "cloud.google.com/go/container/apiv1"
	"cloud.google.com/go/container/apiv1/containerpb"
	"github.com/labstack/echo/v4"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// handleDiscoverGKE connects to Google Cloud, finds clusters in a project, and saves configs
func handleDiscoverGKE(kubeDir string) echo.HandlerFunc {
	return func(c echo.Context) error {
		// [SECURITY] Block this action if not in Admin mode
		if !CurrentConfig.IsAdmin {
			log.Println("⛔ Blocked GKE discovery attempt (Guest Mode)")
			return c.Redirect(302, "/clusters?error=action_not_allowed_in_guest_mode")
		}

		projectID := c.QueryParam("project")
		if projectID == "" {
			return c.Redirect(302, "/clusters?error=missing_gcp_project_id")
		}

		log.Printf("🔍 Connecting to GCP Project: %s...", projectID)

		ctx := context.Background()
		// Create the Client
		// Note: This relies on "Application Default Credentials" (ADC).
		// Ensure you have run 'gcloud auth application-default login' on your machine
		// or set GOOGLE_APPLICATION_CREDENTIALS env var.
		client, err := container.NewClusterManagerClient(ctx)
		if err != nil {
			return c.Redirect(302, fmt.Sprintf("/clusters?error=gcp_client_failed: %v", err))
		}
		defer client.Close()

		// List Clusters
		// Parent format: "projects/{project_id}/locations/-" 
		// The "-" location wildcard lists clusters in all zones/regions for the project.
		req := &containerpb.ListClustersRequest{
			Parent: fmt.Sprintf("projects/%s/locations/-", projectID),
		}

		resp, err := client.ListClusters(ctx, req)
		if err != nil {
			return c.Redirect(302, fmt.Sprintf("/clusters?error=gcp_list_failed: %v", err))
		}

		if len(resp.Clusters) == 0 {
			return c.Redirect(302, "/clusters?error=no_gke_clusters_found")
		}

		count := 0
		for _, cl := range resp.Clusters {
			clusterName := cl.Name
			
			// 4. Build the Kubeconfig Object
			kubeConfig := clientcmdapi.NewConfig()

			// Cluster Data
			// GKE provides the endpoint and the MasterAuth (CA Certificate)
			caData, _ := base64.StdEncoding.DecodeString(cl.MasterAuth.ClusterCaCertificate)
			
			kubeConfig.Clusters[clusterName] = &clientcmdapi.Cluster{
				Server:                   "https://" + cl.Endpoint, // GKE endpoint doesn't include scheme
				CertificateAuthorityData: caData,
			}

			// Auth Info (CHANGED: Use Native AuthProvider instead of Exec)
			// This tells client-go to use the internal GCP plugin.
			// It will automatically reuse the Application Default Credentials (ADC)
			// that the app is already using for discovery.
			kubeConfig.AuthInfos[clusterName] = &clientcmdapi.AuthInfo{
				AuthProvider: &clientcmdapi.AuthProviderConfig{
					Name: "gcp",
					Config: map[string]string{
						"scopes": "https://www.googleapis.com/auth/cloud-platform",
					},
				},
			}

			// Context
			kubeConfig.Contexts[clusterName] = &clientcmdapi.Context{
				Cluster:  clusterName,
				AuthInfo: clusterName,
			}
			kubeConfig.CurrentContext = clusterName

			// 5. Save to disk
			filename := fmt.Sprintf("config-ops-gke-%s.yaml", clusterName)
			fullPath := filepath.Join(kubeDir, filename)

			err = clientcmd.WriteToFile(*kubeConfig, fullPath)
			if err != nil {
				log.Printf("Error saving kubeconfig for %s: %v", clusterName, err)
			} else {
				count++
				log.Printf("✅ Imported GKE Cluster: %s", clusterName)
			}
		}

		return c.Redirect(302, fmt.Sprintf("/clusters?success=imported_%d_gke_clusters", count))
	}
}