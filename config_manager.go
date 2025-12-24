package main

import (
	"fmt"
	"path/filepath"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ClusterConfig represents a normalized kubeconfig source (File or DB)
type ClusterConfig struct {
	Name        string // Unique ID (Filename for files, Name for DB)
	ContextName string // The internal K8s context name
	IsFile      bool
	Path        string
	Content     []byte
}

// ToRestConfig converts the abstract config into a usable Kubernetes REST config
func (c *ClusterConfig) ToRestConfig() (*rest.Config, error) {
	if c.IsFile {
		return clientcmd.BuildConfigFromFlags("", c.Path)
	}
	return clientcmd.RESTConfigFromKubeConfig(c.Content)
}

// LoadAllConfigs fetches configs from both Disk and DB (For Lists)
func LoadAllConfigs(pattern string) ([]ClusterConfig, error) {
	var configs []ClusterConfig

	// 1. Load from Disk
	files, _ := filepath.Glob(pattern)
	for _, f := range files {
		ctxName := filepath.Base(f)
		
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		loadingRules.ExplicitPath = f
		overrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
		raw, err := kubeConfig.RawConfig()
		
		if err == nil && raw.CurrentContext != "" {
			ctxName = raw.CurrentContext
		}

		configs = append(configs, ClusterConfig{
			Name:        filepath.Base(f), // This matches the ?c=... query param
			ContextName: ctxName,
			IsFile:      true,
			Path:        f,
		})
	}

	// 2. Load from Database (One single request for all available configs)
	if DB != nil {
		var dbConfigs []KubeConfig
		// Check table existence to be safe during startup
		if DB.Migrator().HasTable(&KubeConfig{}) {
			if err := DB.Find(&dbConfigs).Error; err == nil {
				for _, c := range dbConfigs {
					// Parse the content to extract real Context Name
					realContext := c.Name // Default to DB name
					parsed, err := clientcmd.Load([]byte(c.Content))
					if err == nil && parsed.CurrentContext != "" {
						realContext = parsed.CurrentContext
					}

					configs = append(configs, ClusterConfig{
						Name:        c.Name, 
						ContextName: realContext, 
						IsFile:      false,
						Content:     []byte(c.Content),
					})
				}
			}
		}
	}

	return configs, nil
}

// GetClusterConfig finds a specific config by context name (For Status/Details)
// Optimized to ONLY scan files as requested (Database check skipped)
func GetClusterConfig(pattern, contextName string) (*ClusterConfig, error) {
	// 1. Try Disk (Scan files)
	files, _ := filepath.Glob(pattern)
	for _, f := range files {
		ctxName := filepath.Base(f)
		
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		loadingRules.ExplicitPath = f
		overrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
		raw, err := kubeConfig.RawConfig()
		
		if err == nil && raw.CurrentContext != "" {
			ctxName = raw.CurrentContext
		}

		if ctxName == contextName || filepath.Base(f) == contextName {
			return &ClusterConfig{
				Name:        filepath.Base(f),
				ContextName: ctxName,
				IsFile:      true,
				Path:        f,
			}, nil
		}
	}

	// [Optimization] Database check skipped for single-cluster lookups 
	// as per requirement to avoid N+1 DB queries during status checks.

	return nil, fmt.Errorf("cluster config not found for context: %s", contextName)
}