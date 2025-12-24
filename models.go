package main

import (
    "gorm.io/gorm"
)

type User struct {
    gorm.Model
    Username string `gorm:"uniqueIndex"`
    Password string // Bcrypt hash
    Role     string // "admin" or "viewer"
    Configs  []KubeConfig
}

type KubeConfig struct {
    gorm.Model
    UserID   uint
    Name     string // e.g., "prod-cluster"
    Content  string `gorm:"type:text"` // The actual yaml content
    Filename string // Virtual filename for the backend logic
}