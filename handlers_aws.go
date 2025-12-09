package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/labstack/echo/v4"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// handleDiscoverEKS connects to AWS, finds clusters, and saves configs
func handleDiscoverEKS(kubeDir string, region string) echo.HandlerFunc {
	return func(c echo.Context) error {
		// [SECURITY] Block this action if not in Admin mode
		if !CurrentConfig.IsAdmin {
			log.Println("⛔ Blocked EKS discovery attempt (Guest Mode)")
			return c.Redirect(302, "/clusters?error=action_not_allowed_in_guest_mode")
		}

		// 1. Load AWS Configuration (looks for env vars, profile, or default chain)
		// We default to us-east-1 if not specified, but you can pass it in query
		targetRegion := c.QueryParam("region")
		if targetRegion == "" {
			targetRegion = region // Use default passed from main if query empty
		}
		if targetRegion == "" {
			targetRegion = "us-east-1" // Fallback
		}

		log.Printf("🔍 connecting to AWS in region %s...", targetRegion)

		cfg, err := config.LoadDefaultConfig(context.TODO(), config.WithRegion(targetRegion))
		if err != nil {
			return c.Redirect(302, fmt.Sprintf("/clusters?error=aws_config_failed: %v", err))
		}

		svc := eks.NewFromConfig(cfg)

		// 2. List Clusters
		input := &eks.ListClustersInput{}
		result, err := svc.ListClusters(context.TODO(), input)
		if err != nil {
			return c.Redirect(302, fmt.Sprintf("/clusters?error=aws_list_failed: %v", err))
		}

		if len(result.Clusters) == 0 {
			return c.Redirect(302, "/clusters?error=no_eks_clusters_found")
		}

		// 3. Describe each cluster to get connection details
		count := 0
		for _, clusterName := range result.Clusters {
			descInput := &eks.DescribeClusterInput{Name: aws.String(clusterName)}
			descResult, err := svc.DescribeCluster(context.TODO(), descInput)
			if err != nil {
				log.Printf("Error describing cluster %s: %v", clusterName, err)
				continue
			}

			cl := descResult.Cluster

			// 4. Build the Kubeconfig Object
			// We construct a config that uses 'aws eks get-token' for auth
			kubeConfig := clientcmdapi.NewConfig()

			// Cluster Data
			// Note: EKS usually provides CA data. We handle the pointer safely.
			if cl.CertificateAuthority != nil && cl.CertificateAuthority.Data != nil {
				caData, _ := base64.StdEncoding.DecodeString(*cl.CertificateAuthority.Data)
				kubeConfig.Clusters[clusterName] = &clientcmdapi.Cluster{
					Server:                   *cl.Endpoint,
					CertificateAuthorityData: caData,
				}
			} else {
				// Fallback if no CA data (skip verification or bare endpoint)
				kubeConfig.Clusters[clusterName] = &clientcmdapi.Cluster{
					Server:                *cl.Endpoint,
					InsecureSkipTLSVerify: true,
				}
			}

			// Auth Info (Exec Plugin)
			kubeConfig.AuthInfos[clusterName] = &clientcmdapi.AuthInfo{
				Exec: &clientcmdapi.ExecConfig{
					APIVersion: "client.authentication.k8s.io/v1beta1",
					Command:    "aws",
					Args: []string{
						"eks",
						"get-token",
						"--cluster-name",
						clusterName,
						"--region",
						targetRegion,
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
			// We use the 'config-ops' prefix so your app automatically picks it up
			filename := fmt.Sprintf("config-ops-eks-%s.yaml", clusterName)
			fullPath := filepath.Join(kubeDir, filename)

			err = clientcmd.WriteToFile(*kubeConfig, fullPath)
			if err != nil {
				log.Printf("Error saving kubeconfig for %s: %v", clusterName, err)
			} else {
				count++
				log.Printf("✅ Imported EKS Cluster: %s", clusterName)
			}
		}

		// 6. Refresh and Redirect
		return c.Redirect(302, fmt.Sprintf("/clusters?success=imported_%d_clusters", count))
	}
}