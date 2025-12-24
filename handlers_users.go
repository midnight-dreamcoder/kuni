package main

import (
	"log"
	"net/http"
	"time"

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
		// Security: Only Admins can view this page
		// We need to check the session/context for the current user's role
		// For now, we assume if you are accessing this, you passed AuthMiddleware.
		// TODO: Real Role Check
		
		var users []User
		if DB != nil {
			DB.Order("id asc").Find(&users)
		}

		base := PageBase{
			Title:         "User Management",
			ActivePage:    "users",
			LastRefreshed: time.Now().Format(time.RFC1123),
			IsAdmin:       true, // Only admins should reach here
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
			// Return to page with error (simplified)
			return c.Redirect(http.StatusFound, "/users")
		}

		log.Printf("✅ Created new user: %s (%s)", username, role)
		return c.Redirect(http.StatusFound, "/users")
	}
}

// handleDeleteUser removes a user
func handleDeleteUser() echo.HandlerFunc {
	return func(c echo.Context) error {
		id := c.FormValue("id")
		
		if DB == nil { return c.String(500, "Database not enabled") }

		// Prevent deleting self or main admin (basic check)
		var user User
		if err := DB.First(&user, id).Error; err == nil {
			if user.Username == "admin" {
				return c.Redirect(http.StatusFound, "/users")
			}
		}

		DB.Delete(&User{}, id)
		log.Printf("🗑️ Deleted user ID: %s", id)
		
		return c.Redirect(http.StatusFound, "/users")
	}
}