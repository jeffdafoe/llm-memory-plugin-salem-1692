package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// serializeUser builds the list-view user object.
func serializeUser(row pgx.Row) (map[string]interface{}, error) {
	var id, username string
	var email *string
	var rolesJSON []byte
	var isActive bool
	var createdAt time.Time
	var lastLoginAt *time.Time
	var alias *string

	err := row.Scan(&id, &username, &email, &rolesJSON, &isActive, &createdAt, &lastLoginAt, &alias)
	if err != nil {
		return nil, err
	}

	var roles []string
	json.Unmarshal(rolesJSON, &roles)

	result := map[string]interface{}{
		"id":          id,
		"username":    username,
		"email":       email,
		"roles":       roles,
		"isActive":    isActive,
		"createdAt":   createdAt.Format(time.RFC3339),
		"lastLoginAt": nil,
		"alias":       alias,
	}
	if lastLoginAt != nil {
		result["lastLoginAt"] = lastLoginAt.Format(time.RFC3339)
	}
	return result, nil
}

// handleListUsers returns a paginated list of users (admin only).
func (app *App) handleListUsers(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	search := r.URL.Query().Get("search")
	offset := (page - 1) * limit

	// Build query with optional search
	var whereClause string
	var args []interface{}
	argIdx := 1

	if search != "" {
		pattern := "%" + search + "%"
		whereClause = fmt.Sprintf(
			` WHERE u.username ILIKE $%d OR u.email ILIKE $%d OR p.alias ILIKE $%d`,
			argIdx, argIdx+1, argIdx+2,
		)
		args = append(args, pattern, pattern, pattern)
		argIdx += 3
	}

	// Count total
	countQuery := `SELECT COUNT(*) FROM "user" u LEFT JOIN user_profile p ON p.user_id = u.id` + whereClause
	var total int
	app.DB.QueryRow(r.Context(), countQuery, args...).Scan(&total)

	// Fetch page
	dataQuery := fmt.Sprintf(
		`SELECT u.id, u.username, u.email, u.roles, u.is_active, u.created_at, u.last_login_at,
		        p.alias
		 FROM "user" u
		 LEFT JOIN user_profile p ON p.user_id = u.id
		 %s
		 ORDER BY u.created_at DESC
		 LIMIT $%d OFFSET $%d`,
		whereClause, argIdx, argIdx+1,
	)
	args = append(args, limit, offset)

	rows, err := app.DB.Query(r.Context(), dataQuery, args...)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var users []map[string]interface{}
	for rows.Next() {
		var id, username string
		var email *string
		var rolesJSON []byte
		var isActive bool
		var createdAt time.Time
		var lastLoginAt *time.Time
		var alias *string

		if err := rows.Scan(&id, &username, &email, &rolesJSON, &isActive, &createdAt, &lastLoginAt, &alias); err != nil {
			continue
		}

		var roles []string
		json.Unmarshal(rolesJSON, &roles)

		user := map[string]interface{}{
			"id":          id,
			"username":    username,
			"email":       email,
			"roles":       roles,
			"isActive":    isActive,
			"createdAt":   createdAt.Format(time.RFC3339),
			"lastLoginAt": nil,
			"alias":       alias,
		}
		if lastLoginAt != nil {
			user["lastLoginAt"] = lastLoginAt.Format(time.RFC3339)
		}
		users = append(users, user)
	}

	if users == nil {
		users = []map[string]interface{}{}
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"users": users,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

// handleGetUser returns a single user with full profile details (admin only).
func (app *App) handleGetUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var username string
	var email *string
	var rolesJSON []byte
	var isActive bool
	var createdAt time.Time
	var lastLoginAt *time.Time
	// Profile fields
	var alias, bio, entryMessage, exitMessage, avatarUrl, preferredColor, timezone *string
	var gender string

	err := app.DB.QueryRow(r.Context(),
		`SELECT u.id, u.username, u.email, u.roles, u.is_active, u.created_at, u.last_login_at,
		        p.alias, p.gender, p.entry_message, p.exit_message, p.bio, p.avatar_url,
		        p.preferred_color, p.timezone
		 FROM "user" u
		 LEFT JOIN user_profile p ON p.user_id = u.id
		 WHERE u.id = $1`,
		id,
	).Scan(&id, &username, &email, &rolesJSON, &isActive, &createdAt, &lastLoginAt,
		&alias, &gender, &entryMessage, &exitMessage, &bio, &avatarUrl,
		&preferredColor, &timezone)

	if err != nil {
		if err == pgx.ErrNoRows {
			jsonError(w, "User not found.", http.StatusNotFound)
			return
		}
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	var roles []string
	json.Unmarshal(rolesJSON, &roles)

	result := map[string]interface{}{
		"id":          id,
		"username":    username,
		"email":       email,
		"roles":       roles,
		"isActive":    isActive,
		"createdAt":   createdAt.Format(time.RFC3339),
		"lastLoginAt": nil,
		"alias":       alias,
		"profile": map[string]interface{}{
			"alias":          alias,
			"gender":         gender,
			"entryMessage":   entryMessage,
			"exitMessage":    exitMessage,
			"bio":            bio,
			"avatarUrl":      avatarUrl,
			"preferredColor": preferredColor,
			"timezone":       timezone,
		},
	}
	if lastLoginAt != nil {
		result["lastLoginAt"] = lastLoginAt.Format(time.RFC3339)
	}

	jsonResponse(w, http.StatusOK, result)
}

// handleUpdateUser updates a user and/or their profile (admin only).
func (app *App) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var input map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Check user exists
	var exists bool
	app.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM "user" WHERE id = $1)`, id).Scan(&exists)
	if !exists {
		jsonError(w, "User not found.", http.StatusNotFound)
		return
	}

	tx, err := app.DB.Begin(r.Context())
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	// Update user fields
	var userUpdates []string
	var userArgs []interface{}
	userArgIdx := 1

	if email, ok := input["email"]; ok {
		if email == "" {
			userUpdates = append(userUpdates, fmt.Sprintf("email = $%d", userArgIdx))
			userArgs = append(userArgs, nil)
		} else {
			userUpdates = append(userUpdates, fmt.Sprintf("email = $%d", userArgIdx))
			userArgs = append(userArgs, email)
		}
		userArgIdx++
	}

	if roles, ok := input["roles"]; ok {
		rolesJSON, _ := json.Marshal(roles)
		userUpdates = append(userUpdates, fmt.Sprintf("roles = $%d", userArgIdx))
		userArgs = append(userArgs, string(rolesJSON))
		userArgIdx++
	}

	if isActive, ok := input["isActive"]; ok {
		userUpdates = append(userUpdates, fmt.Sprintf("is_active = $%d", userArgIdx))
		userArgs = append(userArgs, isActive)
		userArgIdx++
	}

	if password, ok := input["password"].(string); ok && password != "" {
		hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			jsonError(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		userUpdates = append(userUpdates, fmt.Sprintf("password = $%d", userArgIdx))
		userArgs = append(userArgs, string(hashed))
		userArgIdx++
	}

	if len(userUpdates) > 0 {
		userArgs = append(userArgs, id)
		query := fmt.Sprintf(
			`UPDATE "user" SET %s WHERE id = $%d`,
			strings.Join(userUpdates, ", "), userArgIdx,
		)
		if _, err := tx.Exec(r.Context(), query, userArgs...); err != nil {
			jsonError(w, "Failed to update user", http.StatusInternalServerError)
			return
		}
	}

	// Update profile fields
	var profileUpdates []string
	var profileArgs []interface{}
	profileArgIdx := 1

	profileFields := map[string]string{
		"alias":          "alias",
		"bio":            "bio",
		"entryMessage":   "entry_message",
		"exitMessage":    "exit_message",
		"preferredColor": "preferred_color",
		"timezone":       "timezone",
	}

	for jsonField, dbField := range profileFields {
		if val, ok := input[jsonField]; ok {
			if val == "" {
				profileUpdates = append(profileUpdates, fmt.Sprintf("%s = $%d", dbField, profileArgIdx))
				profileArgs = append(profileArgs, nil)
			} else {
				profileUpdates = append(profileUpdates, fmt.Sprintf("%s = $%d", dbField, profileArgIdx))
				profileArgs = append(profileArgs, val)
			}
			profileArgIdx++
		}
	}

	if len(profileUpdates) > 0 {
		profileUpdates = append(profileUpdates, fmt.Sprintf("updated_at = $%d", profileArgIdx))
		profileArgs = append(profileArgs, time.Now())
		profileArgIdx++

		profileArgs = append(profileArgs, id)
		query := fmt.Sprintf(
			`UPDATE user_profile SET %s WHERE user_id = $%d`,
			strings.Join(profileUpdates, ", "), profileArgIdx,
		)
		if _, err := tx.Exec(r.Context(), query, profileArgs...); err != nil {
			jsonError(w, "Failed to update profile", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Return the updated user
	app.handleGetUser(w, r)
}

// handleDeleteUser deletes a user and their profile (admin only).
func (app *App) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	result, err := app.DB.Exec(r.Context(),
		`DELETE FROM "user" WHERE id = $1`, id,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if result.RowsAffected() == 0 {
		jsonError(w, "User not found.", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
