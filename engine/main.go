package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// App holds shared dependencies for all handlers.
type App struct {
	DB           *pgxpool.Pool
	LLMMemoryURL string // base URL for llm-memory auth verification
	Hub          *EventHub
}

func main() {
	// Required environment variables
	databaseURL := requireEnv("DATABASE_URL")
	port := getEnv("PORT", "8080")
	llmMemoryURL := getEnv("LLM_MEMORY_URL", "http://127.0.0.1:3100")

	// Connect to database
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	app := &App{
		DB:           pool,
		LLMMemoryURL: llmMemoryURL,
		Hub:          NewEventHub(),
	}

	// Build router
	mux := http.NewServeMux()

	// Asset catalog (public — needed by client on load before auth)
	mux.HandleFunc("GET /api/assets", app.handleListAssets)

	// All other routes require llm-memory auth (salem realm membership)
	mux.HandleFunc("GET /api/me", app.requireLLMMemory(app.handleVillageMe))
	mux.HandleFunc("GET /api/village/agents", app.requireLLMMemory(app.handleListVillageAgents))
	mux.HandleFunc("POST /api/village/agents/{id}/move", app.requireLLMMemory(app.handleMoveAgent))
	mux.HandleFunc("GET /api/village/objects", app.requireLLMMemory(app.handleListVillageObjects))
	mux.HandleFunc("POST /api/village/objects", app.requireLLMMemory(app.handleCreateVillageObject))
	mux.HandleFunc("POST /api/village/objects/bulk", app.requireLLMMemory(app.handleBulkCreateVillageObjects))
	mux.HandleFunc("DELETE /api/village/objects/{id}", app.requireLLMMemory(app.handleDeleteVillageObject))
	mux.HandleFunc("PATCH /api/village/objects/{id}/owner", app.requireLLMMemory(app.handleSetVillageObjectOwner))
	mux.HandleFunc("PATCH /api/village/objects/{id}/state", app.requireLLMMemory(app.handleSetVillageObjectState))
	mux.HandleFunc("PATCH /api/village/objects/{id}/position", app.requireLLMMemory(app.handleMoveVillageObject))

	// Terrain grid
	mux.HandleFunc("GET /api/village/terrain", app.requireLLMMemory(app.handleGetTerrain))
	mux.HandleFunc("PUT /api/village/terrain", app.requireLLMMemory(app.handleSaveTerrain))

	// WebSocket — real-time world events stream
	mux.HandleFunc("GET /api/village/events", app.handleVillageEvents)

	// CORS middleware for Godot web client
	handler := corsMiddleware(mux)

	server := &http.Server{
		Addr:        ":" + port,
		Handler:     handler,
		ReadTimeout: 10 * time.Second,
		// No WriteTimeout — WebSocket connections are long-lived.
		// Individual write deadlines are set per-message in the WS handler.
		IdleTimeout: 120 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		log.Println("Shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	log.Printf("Salem 1692 engine listening on :%s", port)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

// CORS middleware — allows the Angular client to make cross-origin requests.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// requireEnv reads an environment variable or exits if missing.
func requireEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		log.Fatalf("Required environment variable %s is not set", key)
	}
	return val
}

// getEnv reads an environment variable with a fallback default.
func getEnv(key, fallback string) string {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	return val
}

