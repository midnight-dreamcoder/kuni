package main

import (
	"fmt"
	"path/filepath"
	"sort"

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
		loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: c.Path}
		configOverrides := &clientcmd.ConfigOverrides{}
		if c.ContextName != "" {
			configOverrides.CurrentContext = c.ContextName
		}
		return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides).ClientConfig()
	}

	config, err := clientcmd.Load(c.Content)
	if err != nil {
		return nil, err
	}
	configOverrides := &clientcmd.ConfigOverrides{}
	if c.ContextName != "" {
		configOverrides.CurrentContext = c.ContextName
	}
	return clientcmd.NewDefaultClientConfig(*config, configOverrides).ClientConfig()
}

// LoadAllConfigs fetches configs from both Disk and DB (For Lists)
func LoadAllConfigs(pattern string) ([]ClusterConfig, error) {
	var configs []ClusterConfig

	// 1. Load from Disk
	files, _ := filepath.Glob(pattern)
	for _, f := range files {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		loadingRules.ExplicitPath = f
		overrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
		raw, err := kubeConfig.RawConfig()

		if err == nil {
			if len(raw.Contexts) > 0 {
				var ctxNames []string
				for k := range raw.Contexts {
					ctxNames = append(ctxNames, k)
				}
				sort.Strings(ctxNames)
				for _, ctxName := range ctxNames {
					configs = append(configs, ClusterConfig{
						Name:        filepath.Base(f),
						ContextName: ctxName,
						IsFile:      true,
						Path:        f,
					})
				}
			} else {
				// Fallback
				ctxName := filepath.Base(f)
				if raw.CurrentContext != "" {
					ctxName = raw.CurrentContext
				}
				configs = append(configs, ClusterConfig{
					Name:        filepath.Base(f),
					ContextName: ctxName,
					IsFile:      true,
					Path:        f,
				})
			}
		}
	}

	// 2. Load from Database (One single request for all available configs)
	if DB != nil {
		var dbConfigs []KubeConfig
		// Check table existence to be safe during startup
		if DB.Migrator().HasTable(&KubeConfig{}) {
			if err := DB.Find(&dbConfigs).Error; err == nil {
				for _, c := range dbConfigs {
					parsed, err := clientcmd.Load([]byte(c.Content))
					if err == nil && len(parsed.Contexts) > 0 {
						var ctxNames []string
						for k := range parsed.Contexts {
							ctxNames = append(ctxNames, k)
						}
						sort.Strings(ctxNames)
						for _, ctxName := range ctxNames {
							configs = append(configs, ClusterConfig{
								Name:        c.Name,
								ContextName: ctxName,
								IsFile:      false,
								Content:     []byte(c.Content),
							})
						}
					} else {
						configs = append(configs, ClusterConfig{
							Name:        c.Name,
							ContextName: c.Name,
							IsFile:      false,
							Content:     []byte(c.Content),
						})
					}
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
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		loadingRules.ExplicitPath = f
		overrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
		raw, err := kubeConfig.RawConfig()

		if err == nil {
			if _, ok := raw.Contexts[contextName]; ok {
				return &ClusterConfig{
					Name:        filepath.Base(f),
					ContextName: contextName,
					IsFile:      true,
					Path:        f,
				}, nil
			}
			if filepath.Base(f) == contextName {
				return &ClusterConfig{
					Name:        filepath.Base(f),
					ContextName: raw.CurrentContext,
					IsFile:      true,
					Path:        f,
				}, nil
			}
		}
	}

	// [Optimization] Database check skipped for single-cluster lookups
	// as per requirement to avoid N+1 DB queries during status checks.

	return nil, fmt.Errorf("cluster config not found for context: %s", contextName)
}
