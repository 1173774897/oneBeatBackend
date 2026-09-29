package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"onebeat/store-api/internal/database"
)

const databaseRequestTimeout = 3 * time.Second

type responseEnvelope struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type testResponse struct {
	Service     string `json:"service"`
	Status      string `json:"status"`
	Scheme      string `json:"scheme"`
	Environment string `json:"environment"`
	Version     string `json:"version"`
}

type databaseResponse struct {
	Status           string    `json:"status"`
	Database         string    `json:"database"`
	User             string    `json:"user"`
	ServerTime       time.Time `json:"serverTime"`
	MigrationVersion int64     `json:"migrationVersion"`
	MigrationDirty   bool      `json:"migrationDirty"`
}

type databaseStore interface {
	Ping(context.Context) error
	Status(context.Context) (database.Status, error)
}

// NewHandler returns the HTTP surface for the store API.
func NewHandler(logger *slog.Logger, environment string, version string, store databaseStore) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", getOnly(healthHandler(logger, store)))
	mux.HandleFunc("/api/v1/test", getOnly(testHandler(environment, version)))
	mux.HandleFunc("/api/v1/database/test", getOnly(databaseTestHandler(logger, store)))
	mux.HandleFunc("/", notFoundHandler)
	return requestMiddleware(logger, mux)
}

func healthHandler(logger *slog.Logger, store databaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), databaseRequestTimeout)
		defer cancel()
		if err := store.Ping(ctx); err != nil {
			logger.Error("database health check failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, responseEnvelope{
				Code:    50301,
				Message: "database unavailable",
			})
			return
		}

		writeJSON(w, http.StatusOK, responseEnvelope{
			Code:    0,
			Message: "ok",
			Data: map[string]string{
				"status":   "healthy",
				"database": "connected",
			},
		})
	}
}

func testHandler(environment string, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		writeJSON(w, http.StatusOK, responseEnvelope{
			Code:    0,
			Message: "ok",
			Data: testResponse{
				Service:     "onebeat-store-api",
				Status:      "ready",
				Scheme:      scheme,
				Environment: environment,
				Version:     version,
			},
		})
	}
}

func databaseTestHandler(logger *slog.Logger, store databaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), databaseRequestTimeout)
		defer cancel()

		status, err := store.Status(ctx)
		if err != nil {
			logger.Error("database test failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, responseEnvelope{
				Code:    50302,
				Message: "database test failed",
			})
			return
		}

		writeJSON(w, http.StatusOK, responseEnvelope{
			Code:    0,
			Message: "ok",
			Data: databaseResponse{
				Status:           "connected",
				Database:         status.Name,
				User:             status.User,
				ServerTime:       status.ServerTime,
				MigrationVersion: status.MigrationVersion,
				MigrationDirty:   status.MigrationDirty,
			},
		})
	}
}

func notFoundHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, responseEnvelope{
		Code:    40400,
		Message: "endpoint not found",
	})
}

func getOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, responseEnvelope{
				Code:    40500,
				Message: "method not allowed",
			})
			return
		}
		next(w, r)
	}
}

func requestMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
		logger.Info("request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
	})
}

func writeJSON(w http.ResponseWriter, status int, payload responseEnvelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
}
