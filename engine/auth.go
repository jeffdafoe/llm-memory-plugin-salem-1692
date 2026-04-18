package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// contextKey is a private type for context keys to avoid collisions.
type contextKey string

const userContextKey contextKey = "user"

// AuthUser represents the authenticated user stored in request context.
type AuthUser struct {
	Username string
	Roles    []string
}

// hasRole checks if the user has a specific role.
// ROLE_SALEM_ADMIN > ROLE_SALEM_USER
func (u *AuthUser) hasRole(role string) bool {
	hierarchy := map[string]int{
		"ROLE_SALEM_USER":  0,
		"ROLE_SALEM_ADMIN": 1,
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

// VerifyResult is the outcome of a token validation against llm-memory.
// Reason is set when Valid=false to distinguish missing-token, network failure,
// invalid token, and realm denial — callers translate these to HTTP statuses
// (or, for the WebSocket path, to close codes / push events).
type VerifyResult struct {
	Valid  bool
	Reason string // "missing", "service", "invalid", "realm" — empty when Valid
	User   *AuthUser
}

// verifyLLMMemoryToken validates a session token against the llm-memory API
// and applies salem's realm/role rules. Shared between the HTTP middleware
// and the WebSocket auth/keepalive paths.
func (app *App) verifyLLMMemoryToken(token string) VerifyResult {
	if token == "" {
		return VerifyResult{Valid: false, Reason: "missing"}
	}

	verifyURL := strings.TrimRight(app.LLMMemoryURL, "/") + "/v1/auth/verify"
	body := fmt.Sprintf(`{"token":"%s"}`, token)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(verifyURL, "application/json", strings.NewReader(body))
	if err != nil {
		return VerifyResult{Valid: false, Reason: "service"}
	}
	defer resp.Body.Close()

	var result struct {
		Valid       bool     `json:"valid"`
		Agent       string   `json:"agent"`
		ActorID     int      `json:"actor_id"`
		Realms      []string `json:"realms"`
		SessionKind string   `json:"session_kind"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return VerifyResult{Valid: false, Reason: "service"}
	}
	if !result.Valid {
		return VerifyResult{Valid: false, Reason: "invalid"}
	}

	inRealm := false
	for _, rm := range result.Realms {
		if rm == "salem" {
			inRealm = true
			break
		}
	}
	if !inRealm {
		return VerifyResult{Valid: false, Reason: "realm"}
	}

	// Web sessions (admin login) get admin/editor access; API sessions
	// (agent login) get basic user access.
	roles := []string{"ROLE_SALEM_USER"}
	if result.SessionKind == "web" {
		roles = append(roles, "ROLE_SALEM_ADMIN")
	}
	return VerifyResult{
		Valid: true,
		User: &AuthUser{
			Username: result.Agent,
			Roles:    roles,
		},
	}
}

// requireLLMMemory is middleware that validates an llm-memory session token
// by calling the llm-memory API's /v1/auth/verify endpoint.
// Checks that the user belongs to the "salem" realm.
func (app *App) requireLLMMemory(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res := app.verifyLLMMemoryToken(extractBearerToken(r))
		if !res.Valid {
			switch res.Reason {
			case "service":
				jsonError(w, "Auth service unavailable", http.StatusServiceUnavailable)
			case "realm":
				jsonError(w, "Access denied: not a member of this realm", http.StatusForbidden)
			case "missing":
				jsonError(w, "Missing session token", http.StatusUnauthorized)
			default:
				jsonError(w, "Invalid or expired session token", http.StatusUnauthorized)
			}
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, res.User)
		next(w, r.WithContext(ctx))
	}
}

// getUserFromContext retrieves the authenticated user from the request context.
func getUserFromContext(ctx context.Context) *AuthUser {
	user, _ := ctx.Value(userContextKey).(*AuthUser)
	return user
}

// extractBearerToken pulls the token from an Authorization: Bearer header.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return auth[7:]
}
