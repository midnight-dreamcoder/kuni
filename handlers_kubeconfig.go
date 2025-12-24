package main

import (
	"bytes"
	"io"
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
	"k8s.io/client-go/tools/clientcmd"
)

// KubeConfigPageData holds data for the kubeconfigs.html template
type KubeConfigPageData struct {
	PageBase
	Configs []KubeConfig
	Error   string
}

// handleGetKubeConfigs lists all database-stored kubeconfigs
func handleGetKubeConfigs(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Use GetBaseData for consistent Auth/View context
		base := GetBaseData(c, "Config Management", "kubeconfigs")

		// SECURITY: Only Admins can manage configs
		if !base.IsAdmin {
			return c.Redirect(http.StatusFound, "/overview?error=unauthorized")
		}

		var configs []KubeConfig
		if DB != nil {
			DB.Order("id asc").Find(&configs)
		}

		return c.Render(200, "kubeconfigs.html", KubeConfigPageData{
			PageBase: base,
			Configs:  configs,
		})
	}
}

// handleAddKubeConfig adds a new config (from text or file) to the DB
func handleAddKubeConfig(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Security Check
		isAdmin, _ := c.Get("isAdmin").(bool)
		if !isAdmin {
			return c.String(http.StatusForbidden, "Unauthorized")
		}

		name := c.FormValue("name")
		content := c.FormValue("content")

		// Handle File Upload (Optional override)
		file, err := c.FormFile("file")
		if err == nil {
			// User uploaded a file
			src, err := file.Open()
			if err != nil {
				return c.Redirect(http.StatusFound, "/kubeconfigs?error=read_failed")
			}
			defer src.Close()

			buf := new(bytes.Buffer)
			if _, err := io.Copy(buf, src); err != nil {
				return c.Redirect(http.StatusFound, "/kubeconfigs?error=copy_failed")
			}
			// Use file content
			content = buf.String()
		}

		if content == "" {
			return c.Redirect(http.StatusFound, "/kubeconfigs?error=missing_content")
		}

		// PARSE CONTENT TO FIND CONTEXT NAME
		// If name is missing, we try to use the actual kubernetes context name
		parsedConfig, parseErr := clientcmd.Load([]byte(content))
		if parseErr == nil && parsedConfig.CurrentContext != "" {
			if name == "" {
				name = parsedConfig.CurrentContext
			}
		}

		// Fallback if parsing failed or context is empty and no name provided
		if name == "" {
			if file != nil {
				name = file.Filename
			} else {
				name = "unnamed-context"
			}
		}

		if DB == nil {
			return c.String(500, "Database not enabled")
		}

		// Create in DB
		newConfig := KubeConfig{
			Name:     name,
			Content:  content,
			Filename: "db-entry", // Virtual filename
			UserID:   0,          // System level config
		}

		if err := DB.Create(&newConfig).Error; err != nil {
			log.Printf("Failed to create config: %v", err)
			return c.Redirect(http.StatusFound, "/kubeconfigs?error=creation_failed")
		}

		log.Printf("✅ Added new KubeConfig to DB: %s", name)

		return c.Redirect(http.StatusFound, "/kubeconfigs")
	}
}

// handleDeleteKubeConfig removes a config from the DB
func handleDeleteKubeConfig(pattern string) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Security Check
		isAdmin, _ := c.Get("isAdmin").(bool)
		if !isAdmin {
			return c.String(http.StatusForbidden, "Unauthorized")
		}

		id := c.FormValue("id")
		
		if DB == nil { return c.String(500, "Database not enabled") }

		DB.Delete(&KubeConfig{}, id)
		log.Printf("🗑️ Deleted KubeConfig ID: %s", id)

		return c.Redirect(http.StatusFound, "/kubeconfigs")
	}
}