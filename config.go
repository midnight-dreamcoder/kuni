package main

import (
	"encoding/json"
	"log"
	"os"
	"sync"
)

// AppConfig holds our global configuration
type AppConfig struct {
	AppTitle   string `json:"app_title"`
	IsAdmin    bool   `json:"is_admin"`
	ServerPort string `json:"server_port"`
}

var (
	// Global variable to access config anywhere
	CurrentConfig *AppConfig
	once          sync.Once
)

// LoadConfig reads config.json and populates the global variable
func LoadConfig(path string) {
	once.Do(func() {
		file, err := os.Open(path)
		if err != nil {
			log.Printf("⚠️  Config file not found at %s. Using defaults.", path)
			// Default fallback values
			CurrentConfig = &AppConfig{
				AppTitle:   "K8s Universal Inspector ",
				IsAdmin:    false, // Default to safe mode
				ServerPort: ":8080",
			}
			return
		}
		defer file.Close()

		decoder := json.NewDecoder(file)
		CurrentConfig = &AppConfig{}
		err = decoder.Decode(CurrentConfig)
		if err != nil {
			log.Fatalf("❌ Could not parse config.json: %v", err)
		}
		
		log.Printf("✅ Config loaded. Admin Mode: %v", CurrentConfig.IsAdmin)
	})
}