package main

import (
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

// UserPageData holds data for the users.html template
type UserPageData struct {
	PageBase
	Users []User
	Error string
}

// handleGetUsers lists all users
func handleGetUsers() echo.HandlerFunc {
	return func(c echo.Context) error {
		// UPDATED: Use GetBaseData
		base := GetBaseData(c, "User Management", "users")
		
		// SECURITY CHECK: Explicitly enforce admin access
		if !base.IsAdmin {
			return c.Redirect(http.StatusFound, "/overview?error=unauthorized")
		}

		var users []User
		if DB != nil {
			DB.Order("id asc").Find(&users)
		}

		return c.Render(200, "users.html", UserPageData{
			PageBase: base,
			Users:    users,
		})
	}
}

// handleAddUser creates a new user
func handleAddUser() echo.HandlerFunc {
	return func(c echo.Context) error {
		// Ensure only admins can create users (double check)
		isAdmin, _ := c.Get("isAdmin").(bool)
		if !isAdmin {
			return c.String(http.StatusForbidden, "Unauthorized")
		}

		username := c.FormValue("username")
		password := c.FormValue("password")
		role := c.FormValue("role")

		if DB == nil {
			return c.String(500, "Database not enabled")
		}

		// Hash Password
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return c.String(500, "Error hashing password")
		}

		newUser := User{
			Username: username,
			Password: string(hash),
			Role:     role,
		}

		if err := DB.Create(&newUser).Error; err != nil {
			log.Printf("Failed to create user: %v", err)
			return c.Redirect(http.StatusFound, "/users?error=creation_failed")
		}

		log.Printf("✅ Created new user: %s (%s)", username, role)
		return c.Redirect(http.StatusFound, "/users")
	}
}

// handleDeleteUser removes a user
func handleDeleteUser() echo.HandlerFunc {
	return func(c echo.Context) error {
		// Ensure only admins can delete users
		isAdmin, _ := c.Get("isAdmin").(bool)
		if !isAdmin {
			return c.String(http.StatusForbidden, "Unauthorized")
		}

		id := c.FormValue("id")
		
		if DB == nil { return c.String(500, "Database not enabled") }

		// Prevent deleting self or main admin (basic check)
		var user User
		if err := DB.First(&user, id).Error; err == nil {
			if user.Username == "admin" {
				return c.Redirect(http.StatusFound, "/users?error=cannot_delete_admin")
			}
		}

		DB.Delete(&User{}, id)
		log.Printf("🗑️ Deleted user ID: %s", id)
		
		return c.Redirect(http.StatusFound, "/users")
	}
}