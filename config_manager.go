package main

import (
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

// LoadAllConfigs fetches configs from both Disk and DB
func LoadAllConfigs(pattern string) ([]ClusterConfig, error) {
	var configs []ClusterConfig

	// 1. Load from Disk
	files, _ := filepath.Glob(pattern)
	for _, f := range files {
		// Attempt to parse context name without loading full config
		ctxName := filepath.Base(f)
		
		// Load basic info to get the real context name if possible
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

	// 2. Load from Database (if enabled)
	if DB != nil {
		var dbConfigs []KubeConfig
		// Check table existence to be safe during startup
		if DB.Migrator().HasTable(&KubeConfig{}) {
			if err := DB.Find(&dbConfigs).Error; err == nil {
				for _, c := range dbConfigs {
					configs = append(configs, ClusterConfig{
						Name:        c.Name, // DB Configs use their name as ID
						ContextName: c.Name, // Usually matches context for DB entries
						IsFile:      false,
						Content:     []byte(c.Content),
					})
				}
			}
		}
	}

	return configs, nil
}