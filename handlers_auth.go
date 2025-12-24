package main

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Session struct to hold user details
type Session struct {
	Username string
	Role     string
}

var (
	sessionStore = make(map[string]Session) // SessionID -> Session Struct
	sessionMutex sync.RWMutex
)

// --- 1. Init Default User ---
func CreateDefaultUser() {
	if DB == nil { return } 

	var count int64
	DB.Model(&User{}).Where("username = ?", "admin").Count(&count)
	
	if count == 0 {
		hash, _ := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
		user := User{Username: "admin", Password: string(hash), Role: "admin"}
		if err := DB.Create(&user).Error; err != nil {
			log.Printf("❌ Failed to create admin user: %v", err)
		} else {
			log.Println("✅ Admin user created: admin / admin")
		}
	}
}

// --- 2. Handlers ---

func handleLoginShow() echo.HandlerFunc {
	return func(c echo.Context) error {
		// If logged in, go to dashboard
		if sess, ok := getSession(c); ok {
			log.Printf("User %s already logged in", sess.Username)
			return c.Redirect(http.StatusFound, "/overview")
		}
		return c.Render(200, "login.html", map[string]interface{}{})
	}
}

func handleLoginSubmit() echo.HandlerFunc {
	return func(c echo.Context) error {
		username := c.FormValue("username")
		password := c.FormValue("password")

		var user User
		if err := DB.Where("username = ?", username).First(&user).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return c.Render(200, "login.html", map[string]interface{}{"Error": "User not found"})
			}
			return c.Render(200, "login.html", map[string]interface{}{"Error": "Database error"})
		}

		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
			return c.Render(200, "login.html", map[string]interface{}{"Error": "Invalid password"})
		}

		// Create Session
		sessionID := generateSessionID()
		sessionMutex.Lock()
		sessionStore[sessionID] = Session{Username: user.Username, Role: user.Role}
		sessionMutex.Unlock()

		cookie := new(http.Cookie)
		cookie.Name = "kuni_session"
		cookie.Value = sessionID
		cookie.Expires = time.Now().Add(24 * time.Hour)
		cookie.HttpOnly = true
		cookie.Path = "/"
		c.SetCookie(cookie)

		log.Printf("✅ Login successful: %s (%s)", user.Username, user.Role)
		return c.Redirect(http.StatusFound, "/overview")
	}
}

func handleLogout() echo.HandlerFunc {
	return func(c echo.Context) error {
		cookie, err := c.Cookie("kuni_session")
		if err == nil {
			sessionMutex.Lock()
			delete(sessionStore, cookie.Value)
			sessionMutex.Unlock()
		}
		// Clear Cookie
		c.SetCookie(&http.Cookie{Name: "kuni_session", MaxAge: -1, Path: "/"})
		return c.Redirect(http.StatusFound, "/login")
	}
}

// --- 3. Middleware ---

// OptionalAuthMiddleware identifies the user but allows Guests to proceed.
func OptionalAuthMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Defaults for Guest
		c.Set("username", "")
		c.Set("role", "guest")
		c.Set("isAdmin", false)

		cookie, err := c.Cookie("kuni_session")
		if err == nil {
			sessionMutex.RLock()
			sess, exists := sessionStore[cookie.Value]
			sessionMutex.RUnlock()
			
			if exists {
				c.Set("username", sess.Username)
				c.Set("role", sess.Role)
				c.Set("isAdmin", sess.Role == "admin")
			}
		}
		return next(c)
	}
}

// RequireAdmin middleware strictly blocks non-admins
func RequireAdmin(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		isAdmin, ok := c.Get("isAdmin").(bool)
		if !ok || !isAdmin {
			// If not admin, verify if it's an AJAX request or normal page
			return c.Redirect(http.StatusFound, "/login?error=admin_required")
		}
		return next(c)
	}
}

// Helper to get session securely inside handlers if needed
func getSession(c echo.Context) (Session, bool) {
	cookie, err := c.Cookie("kuni_session")
	if err != nil { return Session{}, false }
	
	sessionMutex.RLock()
	defer sessionMutex.RUnlock()
	sess, ok := sessionStore[cookie.Value]
	return sess, ok
}

func generateSessionID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil { return "error" }
	return hex.EncodeToString(b)
}