package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

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

// NewHandler returns the HTTP surface for the store API.
func NewHandler(logger *slog.Logger, environment string, version string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", getOnly(healthHandler))
	mux.HandleFunc("/api/v1/test", getOnly(testHandler(environment, version)))
	mux.HandleFunc("/", notFoundHandler)
	return requestMiddleware(logger, mux)
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, responseEnvelope{
		Code:    0,
		Message: "ok",
		Data: map[string]string{
			"status": "healthy",
		},
	})
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
