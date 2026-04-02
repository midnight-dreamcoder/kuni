package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB is the global database connection handle.
// Other files can access this variable (e.g., DB.Create(...)).
var DB *gorm.DB

// InitDB initializes the SQLite database connection and performs migrations.
// It is called from main.go after config is loaded.
func InitDB(dbPath string) {
	// 1. If no path is provided in config, run in "File-Only" mode (Legacy behavior)
	if dbPath == "" {
		log.Println("📂 DatabasePath is empty. Running in filesystem-only mode.")
		return
	}

	// 2. Ensure the directory exists (e.g., if path is "data/kuni.db", create "data/")
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatalf("❌ Failed to create database directory: %v", err)
	}

	// 3. Connect to SQLite
	var err error
	DB, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		// Log SQL queries to console (useful for debugging, set to Silent in prod)
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		log.Fatalf("❌ Failed to connect to SQLite database: %v", err)
	}
	log.Printf("✅ Database connected successfully: %s", dbPath)

	// 4. Auto-Migrate Schema
	// This automatically creates 'users' and 'kube_configs' tables based on your models.
	// NOTE: This relies on structs defined in 'models.go'.
	err = DB.AutoMigrate(&User{}, &KubeConfig{})
	if err != nil {
		log.Fatalf("❌ Database migration failed: %v", err)
	}
}