package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// contextKey is a private type for context keys to avoid collisions.
type contextKey string

const userContextKey contextKey = "user"

// AuthUser represents the authenticated user stored in request context.
type AuthUser struct {
	ID       string
	Username string
	Roles    []string
}

// hasRole checks if the user has a specific role, respecting the role hierarchy.
// ROLE_SYSOP > ROLE_MODERATOR > ROLE_MEMBER > ROLE_USER
func (u *AuthUser) hasRole(role string) bool {
	hierarchy := map[string]int{
		"ROLE_USER":      0,
		"ROLE_MEMBER":    1,
		"ROLE_MODERATOR": 2,
		"ROLE_SYSOP":     3,
	}

	requiredLevel, ok := hierarchy[role]
	if !ok {
		return false
	}

	for _, r := range u.Roles {
		if level, exists := hierarchy[r]; exists {
			if level >= requiredLevel {
				return true
			}
		}
	}
	return false
}

// generateJWT creates a signed JWT token for a user.
func (app *App) generateJWT(userID, username string, roles []string) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":      userID,
		"username": username,
		"roles":    roles,
		"iat":      now.Unix(),
		"exp":      now.Add(app.JWTTokenTTL).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(app.JWTPrivateKey)
}

// parseJWT validates a JWT token and returns the claims.
func (app *App) parseJWT(tokenString string) (*AuthUser, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return app.JWTPublicKey, nil
	})

	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	// Extract roles from claims (stored as []interface{})
	var roles []string
	if rawRoles, ok := claims["roles"].([]interface{}); ok {
		for _, r := range rawRoles {
			if s, ok := r.(string); ok {
				roles = append(roles, s)
			}
		}
	}

	return &AuthUser{
		ID:       fmt.Sprintf("%v", claims["sub"]),
		Username: fmt.Sprintf("%v", claims["username"]),
		Roles:    roles,
	}, nil
}

// requireJWT is middleware that validates the JWT and adds the user to context.
func (app *App) requireJWT(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" {
			jsonError(w, "Missing authorization token", http.StatusUnauthorized)
			return
		}

		user, err := app.parseJWT(token)
		if err != nil {
			jsonError(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next(w, r.WithContext(ctx))
	}
}

// requireRole is middleware that checks JWT auth and verifies the user has a role.
func (app *App) requireRole(role string, next http.HandlerFunc) http.HandlerFunc {
	return app.requireJWT(func(w http.ResponseWriter, r *http.Request) {
		user := getUserFromContext(r.Context())
		if user == nil || !user.hasRole(role) {
			jsonError(w, "Access denied", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

// requireLLMMemory is middleware that validates an llm-memory session token
// by calling the llm-memory API's /v1/auth/verify endpoint.
func (app *App) requireLLMMemory(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" {
			jsonError(w, "Missing session token", http.StatusUnauthorized)
			return
		}

		// Call llm-memory to verify the token
		verifyURL := strings.TrimRight(app.LLMMemoryURL, "/") + "/v1/auth/verify"
		body := fmt.Sprintf(`{"token":"%s"}`, token)

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Post(verifyURL, "application/json", strings.NewReader(body))
		if err != nil {
			jsonError(w, "Auth service unavailable", http.StatusServiceUnavailable)
			return
		}
		defer resp.Body.Close()

		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			jsonError(w, "Auth service error", http.StatusServiceUnavailable)
			return
		}

		valid, _ := result["valid"].(bool)
		if !valid {
			jsonError(w, "Invalid or expired session token", http.StatusUnauthorized)
			return
		}

		// Store the llm-memory agent info in context
		agentName, _ := result["agent"].(string)
		user := &AuthUser{
			Username: agentName,
			Roles:    []string{"ROLE_VILLAGE_USER"},
		}

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next(w, r.WithContext(ctx))
	}
}

// getUserFromContext retrieves the authenticated user from the request context.
func getUserFromContext(ctx context.Context) *AuthUser {
	user, _ := ctx.Value(userContextKey).(*AuthUser)
	return user
}

// handleLogin authenticates a user with username/password and returns a JWT.
func (app *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if input.Username == "" || input.Password == "" {
		jsonError(w, "Username and password are required", http.StatusBadRequest)
		return
	}

	// Look up user by username
	var id, username, passwordHash string
	var rolesJSON []byte
	err := app.DB.QueryRow(r.Context(),
		`SELECT id, username, password, roles FROM "user" WHERE username = $1 AND is_active = true`,
		input.Username,
	).Scan(&id, &username, &passwordHash, &rolesJSON)

	if err != nil {
		if err == pgx.ErrNoRows {
			jsonError(w, "Invalid credentials", http.StatusUnauthorized)
			return
		}
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Verify password against bcrypt hash
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(input.Password)); err != nil {
		jsonError(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	// Parse roles
	var roles []string
	json.Unmarshal(rolesJSON, &roles)

	// Update last login time
	app.DB.Exec(r.Context(),
		`UPDATE "user" SET last_login_at = $1 WHERE id = $2`,
		time.Now(), id,
	)

	// Generate JWT
	tokenString, err := app.generateJWT(id, username, roles)
	if err != nil {
		jsonError(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"token": tokenString,
	})
}

// handleMe returns the current authenticated user's info.
func (app *App) handleMe(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())

	// Fetch full user details from DB
	var email *string
	var isActive bool
	var createdAt time.Time
	var lastLoginAt *time.Time
	err := app.DB.QueryRow(r.Context(),
		`SELECT email, is_active, created_at, last_login_at FROM "user" WHERE id = $1`,
		user.ID,
	).Scan(&email, &isActive, &createdAt, &lastLoginAt)

	if err != nil {
		jsonError(w, "User not found", http.StatusNotFound)
		return
	}

	result := map[string]interface{}{
		"id":          user.ID,
		"username":    user.Username,
		"email":       email,
		"roles":       user.Roles,
		"isActive":    isActive,
		"createdAt":   createdAt.Format(time.RFC3339),
		"lastLoginAt": nil,
	}
	if lastLoginAt != nil {
		result["lastLoginAt"] = lastLoginAt.Format(time.RFC3339)
	}

	jsonResponse(w, http.StatusOK, result)
}

// handleRegister creates a new user account.
func (app *App) handleRegister(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Email    string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if input.Username == "" || input.Password == "" {
		jsonError(w, "Username and password are required.", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(input.Username)
	if len(username) < 3 || len(username) > 35 {
		jsonError(w, "Username must be between 3 and 35 characters.", http.StatusBadRequest)
		return
	}

	// Check if username is taken
	var exists bool
	app.DB.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM "user" WHERE username = $1)`,
		username,
	).Scan(&exists)

	if exists {
		jsonError(w, "Username is already taken.", http.StatusConflict)
		return
	}

	// Hash password with bcrypt
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Generate UUID v7
	userID := newUUIDv7()
	profileID := newUUIDv7()
	now := time.Now()
	roles := `["ROLE_USER"]`

	// Insert user and profile in a transaction
	tx, err := app.DB.Begin(r.Context())
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	var emailVal *string
	if input.Email != "" {
		emailVal = &input.Email
	}

	_, err = tx.Exec(r.Context(),
		`INSERT INTO "user" (id, username, email, roles, password, created_at, is_verified, is_active)
		 VALUES ($1, $2, $3, $4, $5, $6, true, true)`,
		userID, username, emailVal, roles, string(hashedPassword), now,
	)
	if err != nil {
		jsonError(w, "Failed to create user", http.StatusInternalServerError)
		return
	}

	_, err = tx.Exec(r.Context(),
		`INSERT INTO user_profile (id, user_id, gender, created_at, updated_at)
		 VALUES ($1, $2, 'unspecified', $3, $3)`,
		profileID, userID, now,
	)
	if err != nil {
		jsonError(w, "Failed to create profile", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusCreated, map[string]interface{}{
		"message":  "Registration successful.",
		"username": username,
	})
}

// resetPassword is a CLI command that resets a user's password.
// Reads DATABASE_URL from the environment and prompts for a new password.
func resetPassword(username string) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	// Check user exists
	var userID string
	err = pool.QueryRow(ctx,
		`SELECT id FROM "user" WHERE username = $1`, username,
	).Scan(&userID)
	if err != nil {
		if err == pgx.ErrNoRows {
			log.Fatalf("User '%s' not found", username)
		}
		log.Fatalf("Database error: %v", err)
	}

	// Prompt for new password
	fmt.Printf("Enter new password for '%s': ", username)
	reader := bufio.NewReader(os.Stdin)
	password, _ := reader.ReadString('\n')
	password = strings.TrimSpace(password)

	if len(password) < 8 {
		log.Fatal("Password must be at least 8 characters")
	}

	// Hash and update
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("Failed to hash password: %v", err)
	}

	_, err = pool.Exec(ctx,
		`UPDATE "user" SET password = $1 WHERE id = $2`,
		string(hashed), userID,
	)
	if err != nil {
		log.Fatalf("Failed to update password: %v", err)
	}

	fmt.Printf("Password reset for '%s'.\n", username)
}
