package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// App holds shared dependencies for all handlers.
type App struct {
	DB             *pgxpool.Pool
	JWTPrivateKey  *rsa.PrivateKey
	JWTPublicKey   *rsa.PublicKey
	JWTTokenTTL    time.Duration
	LLMMemoryURL   string // base URL for llm-memory auth verification
}

func main() {
	// CLI subcommands — check before requiring all env vars
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "reset-password":
			if len(os.Args) < 3 {
				log.Fatal("Usage: zbbs reset-password <username>")
			}
			resetPassword(os.Args[2])
			return
		}
	}

	// Required environment variables
	databaseURL := requireEnv("DATABASE_URL")
	jwtPrivateKeyPath := requireEnv("JWT_PRIVATE_KEY")
	jwtPublicKeyPath := requireEnv("JWT_PUBLIC_KEY")
	port := getEnv("PORT", "8080")
	llmMemoryURL := getEnv("LLM_MEMORY_URL", "http://127.0.0.1:3100")

	// Parse JWT keys
	privateKey, err := loadRSAPrivateKey(jwtPrivateKeyPath)
	if err != nil {
		log.Fatalf("Failed to load JWT private key: %v", err)
	}
	publicKey, err := loadRSAPublicKey(jwtPublicKeyPath)
	if err != nil {
		log.Fatalf("Failed to load JWT public key: %v", err)
	}

	// Connect to database
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	// Verify connection
	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	app := &App{
		DB:            pool,
		JWTPrivateKey: privateKey,
		JWTPublicKey:  publicKey,
		JWTTokenTTL:   time.Hour, // 3600 seconds, same as Symfony config
		LLMMemoryURL:  llmMemoryURL,
	}

	// Build router
	mux := http.NewServeMux()

	// Public routes (no auth)
	mux.HandleFunc("POST /api/login", app.handleLogin)
	mux.HandleFunc("POST /api/register", app.handleRegister)
	mux.HandleFunc("GET /api/settings/public", app.handlePublicSettings)

	// JWT-authenticated routes
	mux.HandleFunc("GET /api/me", app.requireJWT(app.handleMe))
	mux.HandleFunc("GET /api/settings", app.requireRole("ROLE_SYSOP", app.handleListSettings))
	mux.HandleFunc("PUT /api/settings/{key}", app.requireRole("ROLE_SYSOP", app.handleUpdateSetting))
	mux.HandleFunc("GET /api/users", app.requireRole("ROLE_SYSOP", app.handleListUsers))
	mux.HandleFunc("GET /api/users/{id}", app.requireRole("ROLE_SYSOP", app.handleGetUser))
	mux.HandleFunc("PATCH /api/users/{id}", app.requireRole("ROLE_SYSOP", app.handleUpdateUser))
	mux.HandleFunc("DELETE /api/users/{id}", app.requireRole("ROLE_SYSOP", app.handleDeleteUser))

	// llm-memory authenticated routes — village
	mux.HandleFunc("GET /api/village/objects", app.requireLLMMemory(app.handleListVillageObjects))
	mux.HandleFunc("POST /api/village/objects", app.requireLLMMemory(app.handleCreateVillageObject))
	mux.HandleFunc("POST /api/village/objects/bulk", app.requireLLMMemory(app.handleBulkCreateVillageObjects))
	mux.HandleFunc("DELETE /api/village/objects/{id}", app.requireLLMMemory(app.handleDeleteVillageObject))
	mux.HandleFunc("PATCH /api/village/objects/{id}/owner", app.requireLLMMemory(app.handleSetVillageObjectOwner))

	// CORS middleware for Angular client
	handler := corsMiddleware(mux)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
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

	log.Printf("ZBBS server listening on :%s", port)
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

// loadRSAPrivateKey reads and parses a PEM-encoded RSA private key file.
func loadRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading key file: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", path)
	}

	// Try PKCS8 first (Symfony's default), fall back to PKCS1
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		rsaKey, err2 := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("parsing private key: %w (also tried PKCS1: %v)", err, err2)
		}
		return rsaKey, nil
	}

	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is not RSA")
	}
	return rsaKey, nil
}

// loadRSAPublicKey reads and parses a PEM-encoded RSA public key file.
func loadRSAPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading key file: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", path)
	}

	// Try PKIX first, fall back to PKCS1
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		rsaPub, err2 := x509.ParsePKCS1PublicKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("parsing public key: %w (also tried PKCS1: %v)", err, err2)
		}
		return rsaPub, nil
	}

	rsaKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("key is not RSA")
	}
	return rsaKey, nil
}

// extractBearerToken pulls the token from an Authorization: Bearer header.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return auth[7:]
}
