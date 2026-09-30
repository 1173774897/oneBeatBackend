package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"onebeat/store-api/internal/auth"
	"onebeat/store-api/internal/database"
	"onebeat/store-api/internal/store/catalog"
	"onebeat/store-api/internal/store/security"
	storeservice "onebeat/store-api/internal/store/service"
)

const (
	databaseRequestTimeout = 3 * time.Second
	storeRequestTimeout    = 12 * time.Second
	maximumJSONBody        = 2 << 20
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

type huaweiIdentityAuthenticator interface {
	Authenticate(context.Context, string, string) (auth.HuaweiIdentity, error)
}

type userRepository interface {
	UpsertUser(context.Context, []byte, time.Time) (string, error)
}

type StoreServices struct {
	HuaweiIdentityAuthenticator huaweiIdentityAuthenticator
	UserRepository              userRepository
	SessionManager              *auth.SessionManager
	StoreService                *storeservice.Service
	AccountUnionIDPepper        []byte
}

// NewHandler returns the HTTP surface for the store API.
func NewHandler(logger *slog.Logger, environment string, version string, store databaseStore) http.Handler {
	return NewHandlerWithStoreServices(logger, environment, version, store, nil)
}

// NewHandlerWithStoreServices adds authenticated store and IAP routes to the base API.
func NewHandlerWithStoreServices(
	logger *slog.Logger,
	environment string,
	version string,
	store databaseStore,
	services *StoreServices,
) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", getOnly(healthHandler(logger, store)))
	mux.HandleFunc("/api/v1/test", getOnly(testHandler(environment, version)))
	mux.HandleFunc("/api/v1/database/test", getOnly(databaseTestHandler(logger, store)))
	mux.HandleFunc("/api/v1/store/bootstrap", getOnly(storeBootstrapHandler(services)))
	mux.HandleFunc("/api/v1/auth/huawei", postOnly(huaweiAuthHandler(logger, services)))
	mux.HandleFunc("/api/v1/iap/purchases/verify", postOnly(verifyPurchaseHandler(services)))
	mux.HandleFunc("/api/v1/iap/purchases/restore", postOnly(restorePurchasesHandler(services)))
	mux.HandleFunc("/", notFoundHandler)
	return requestMiddleware(logger, mux)
}

func storeBootstrapHandler(services *StoreServices) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if services == nil || services.StoreService == nil || services.SessionManager == nil {
			writeJSON(w, http.StatusOK, responseEnvelope{Code: 0, Message: "ok", Data: catalog.AnonymousBootstrap(time.Now())})
			return
		}
		userID, present, err := optionalUser(r, services.SessionManager)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, responseEnvelope{Code: 40101, Message: "invalid OneBeat session"})
			return
		}
		if !present {
			writeJSON(w, http.StatusOK, responseEnvelope{Code: 0, Message: "ok", Data: catalog.AnonymousBootstrap(time.Now())})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), storeRequestTimeout)
		defer cancel()
		bootstrap, err := services.StoreService.Bootstrap(ctx, userID)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, responseEnvelope{Code: 50301, Message: "store data unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, responseEnvelope{Code: 0, Message: "ok", Data: bootstrap})
	}
}

func huaweiAuthHandler(logger *slog.Logger, services *StoreServices) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if services == nil || services.HuaweiIdentityAuthenticator == nil || services.UserRepository == nil || services.SessionManager == nil {
			writeJSON(w, http.StatusServiceUnavailable, responseEnvelope{Code: 50311, Message: "Huawei account service is not configured"})
			return
		}
		var input struct {
			IDToken           string `json:"idToken"`
			AuthorizationCode string `json:"authorizationCode"`
		}
		if err := decodeJSON(r, &input); err != nil || strings.TrimSpace(input.IDToken) == "" ||
			strings.TrimSpace(input.AuthorizationCode) == "" {
			writeJSON(w, http.StatusBadRequest, responseEnvelope{Code: 40001, Message: "invalid request"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), storeRequestTimeout)
		defer cancel()
		identity, err := services.HuaweiIdentityAuthenticator.Authenticate(ctx, input.IDToken, input.AuthorizationCode)
		if err != nil {
			logHuaweiAuthFailure(logger, err)
			writeJSON(w, http.StatusUnauthorized, responseEnvelope{Code: 40101, Message: "Huawei identity verification failed"})
			return
		}
		userID, err := services.UserRepository.UpsertUser(ctx,
			security.HMACSHA256(services.AccountUnionIDPepper, identity.UnionID), time.Now().UTC())
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, responseEnvelope{Code: 50311, Message: "Huawei account service is temporarily unavailable"})
			return
		}
		accessToken, expiresAt, err := services.SessionManager.Issue(userID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, responseEnvelope{Code: 50001, Message: "session creation failed"})
			return
		}
		writeJSON(w, http.StatusOK, responseEnvelope{Code: 0, Message: "ok", Data: map[string]interface{}{
			"accessToken": accessToken,
			"expiresAt":   expiresAt,
			"user":        map[string]string{"id": userID},
		}})
	}
}

func logHuaweiAuthFailure(logger *slog.Logger, err error) {
	if logger == nil {
		return
	}
	var apiError *auth.HuaweiAPIError
	if errors.As(err, &apiError) {
		logger.Error("huawei identity verification failed",
			"operation", apiError.Operation,
			"http_status", apiError.HTTPStatus,
			"huawei_error", apiError.ErrorCode,
			"huawei_sub_error", apiError.SubError,
		)
		return
	}
	logger.Error("huawei identity verification failed", "reason", err.Error())
}

func verifyPurchaseHandler(services *StoreServices) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := requireUser(w, r, services)
		if !ok {
			return
		}
		var input storeservice.VerifyInput
		if err := decodeJSON(r, &input); err != nil {
			writeJSON(w, http.StatusBadRequest, responseEnvelope{Code: 40001, Message: "invalid request"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), storeRequestTimeout)
		defer cancel()
		bootstrap, err := services.StoreService.VerifyPurchase(ctx, userID, input)
		writeStoreResult(w, bootstrap, err)
	}
}

func restorePurchasesHandler(services *StoreServices) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := requireUser(w, r, services)
		if !ok {
			return
		}
		var input struct {
			Purchases []storeservice.VerifyInput `json:"purchases"`
		}
		if err := decodeJSON(r, &input); err != nil {
			writeJSON(w, http.StatusBadRequest, responseEnvelope{Code: 40001, Message: "invalid request"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), storeRequestTimeout)
		defer cancel()
		bootstrap, err := services.StoreService.RestorePurchases(ctx, userID, input.Purchases)
		writeStoreResult(w, bootstrap, err)
	}
}

func writeStoreResult(w http.ResponseWriter, bootstrap catalog.Bootstrap, err error) {
	if errors.Is(err, storeservice.ErrInvalidPurchase) {
		writeJSON(w, http.StatusUnprocessableEntity, responseEnvelope{Code: 42231, Message: "purchase verification failed"})
		return
	}
	if errors.Is(err, storeservice.ErrHuaweiUnavailable) {
		writeJSON(w, http.StatusServiceUnavailable, responseEnvelope{Code: 50321, Message: "Huawei IAP is temporarily unavailable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, responseEnvelope{Code: 50002, Message: "store operation failed"})
		return
	}
	writeJSON(w, http.StatusOK, responseEnvelope{Code: 0, Message: "ok", Data: bootstrap})
}

func requireUser(w http.ResponseWriter, r *http.Request, services *StoreServices) (string, bool) {
	if services == nil || services.SessionManager == nil || services.StoreService == nil {
		writeJSON(w, http.StatusServiceUnavailable, responseEnvelope{Code: 50301, Message: "store service is not configured"})
		return "", false
	}
	userID, present, err := optionalUser(r, services.SessionManager)
	if err != nil || !present {
		writeJSON(w, http.StatusUnauthorized, responseEnvelope{Code: 40101, Message: "valid OneBeat login is required"})
		return "", false
	}
	return userID, true
}

func optionalUser(r *http.Request, sessions *auth.SessionManager) (string, bool, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return "", false, nil
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", true, errors.New("invalid authorization header")
	}
	claims, err := sessions.Verify(strings.TrimSpace(strings.TrimPrefix(header, prefix)))
	if err != nil {
		return "", true, err
	}
	return claims.Subject, true, nil
}

func decodeJSON(r *http.Request, target interface{}) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maximumJSONBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	return nil
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

func postOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, responseEnvelope{Code: 40500, Message: "method not allowed"})
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
