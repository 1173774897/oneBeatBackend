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
	"onebeat/store-api/internal/store/repository"
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
	UserStatus(context.Context, string) (string, error)
}

// accountDeleter is the account-deletion slice of the store service, kept as an
// interface so the HTTP contract can be tested without a database.
type accountDeleter interface {
	DeleteAccount(context.Context, string) (time.Time, error)
}

type StoreServices struct {
	HuaweiIdentityAuthenticator huaweiIdentityAuthenticator
	UserRepository              userRepository
	SessionManager              *auth.SessionManager
	StoreService                *storeservice.Service
	AccountDeleter              accountDeleter
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
	mux.HandleFunc("/api/v1/store/redemptions", postOnly(redeemHandler(logger, services, newRedemptionLimiter())))
	mux.HandleFunc("/api/v1/auth/huawei", postOnly(huaweiAuthHandler(logger, services)))
	// No purchase/IAP routes: paid purchases are resolved on the device and
	// purchase data must not reach this server (cross-border, 2026-11-03).
	mux.HandleFunc("/api/v1/account/deletion", postOnly(accountDeletionHandler(logger, services)))
	mux.HandleFunc("/", notFoundHandler)
	return requestMiddleware(logger, mux)
}

func redeemHandler(logger *slog.Logger, services *StoreServices, limiter *redemptionLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := requireUser(w, r, services)
		if !ok {
			return
		}
		clientIP := remoteIP(r.RemoteAddr)
		if !limiter.Allow(userID, clientIP, time.Now()) {
			writeJSON(w, http.StatusTooManyRequests, responseEnvelope{Code: 42901, Message: "too many failed redemption attempts"})
			return
		}
		var input storeservice.RedeemInput
		if err := decodeJSON(r, &input); err != nil {
			writeJSON(w, http.StatusBadRequest, responseEnvelope{Code: 40001, Message: "invalid request"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), storeRequestTimeout)
		defer cancel()
		result, err := services.StoreService.Redeem(ctx, userID, input, strings.TrimSpace(r.Header.Get("X-Request-ID")))
		if err == nil {
			writeJSON(w, http.StatusOK, responseEnvelope{Code: 0, Message: "ok", Data: result})
			return
		}
		status, code, message := redemptionErrorResponse(err)
		// Count only malformed or unmatched secrets. Provider failures must not lock
		// legitimate users out; other business conflicts occur after a successful match.
		if errors.Is(err, storeservice.ErrRedemptionInvalid) || errors.Is(err, storeservice.ErrRedemptionRequest) {
			limiter.RecordFailure(userID, clientIP, time.Now())
		}
		if logger != nil {
			logger.Info("store redemption completed", "userId", userID, "resultCode", code)
		}
		writeJSON(w, status, responseEnvelope{Code: code, Message: message})
	}
}

func redemptionErrorResponse(err error) (int, int, string) {
	switch {
	case errors.Is(err, storeservice.ErrRedemptionRequest):
		return http.StatusBadRequest, 40001, "invalid request"
	case errors.Is(err, storeservice.ErrRedemptionNotStarted):
		return http.StatusConflict, 40921, "redemption campaign has not started"
	case errors.Is(err, storeservice.ErrRedemptionInvalid):
		return http.StatusConflict, 40922, "redemption code is invalid or unavailable"
	case errors.Is(err, storeservice.ErrRedemptionAccountLimit):
		return http.StatusConflict, 40923, "redemption has already been used by this account"
	case errors.Is(err, storeservice.ErrRedemptionGlobalLimit):
		return http.StatusConflict, 40924, "redemption campaign quota is exhausted"
	case errors.Is(err, storeservice.ErrRedemptionIdempotencyConflict):
		return http.StatusConflict, 40926, "idempotency key belongs to another redemption"
	default:
		return http.StatusServiceUnavailable, 50301, "store data unavailable"
	}
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
		if active, err := userIsActive(ctx, services, userID); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, responseEnvelope{Code: 50301, Message: "store data unavailable"})
			return
		} else if !active {
			writeJSON(w, http.StatusUnauthorized, responseEnvelope{Code: 40101, Message: "invalid OneBeat session"})
			return
		}
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

// accountDeletionHandler deletes the caller's Store account. It accepts a
// session of an already deleted account and answers with the original time, so
// a client that lost the first response can retry; every other route rejects
// such a session (requireUser).
func accountDeletionHandler(logger *slog.Logger, services *StoreServices) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if services == nil || services.SessionManager == nil || services.AccountDeleter == nil {
			writeJSON(w, http.StatusServiceUnavailable, responseEnvelope{Code: 50301, Message: "store service is not configured"})
			return
		}
		userID, present, err := optionalUser(r, services.SessionManager)
		if err != nil || !present {
			writeJSON(w, http.StatusUnauthorized, responseEnvelope{Code: 40101, Message: "valid OneBeat login is required"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), storeRequestTimeout)
		defer cancel()
		deletedAt, err := services.AccountDeleter.DeleteAccount(ctx, userID)
		status, code, message := http.StatusOK, 0, "ok"
		switch {
		case err == nil:
		case errors.Is(err, repository.ErrUserNotFound):
			status, code, message = http.StatusUnauthorized, 40101, "valid OneBeat login is required"
		default:
			status, code, message = http.StatusServiceUnavailable, 50301, "store data unavailable"
		}
		if logger != nil {
			logger.Info("store account deletion completed", "userId", userID, "resultCode", code)
		}
		if err != nil {
			writeJSON(w, status, responseEnvelope{Code: code, Message: message})
			return
		}
		writeJSON(w, status, responseEnvelope{Code: code, Message: message,
			Data: map[string]time.Time{"deletedAt": deletedAt}})
	}
}

// userIsActive rejects sessions of deleted or disabled accounts. Sessions are
// stateless JWTs that stay valid until expiry, so this is the revocation point.
func userIsActive(ctx context.Context, services *StoreServices, userID string) (bool, error) {
	if services.UserRepository == nil {
		return false, errors.New("user repository is not configured")
	}
	status, err := services.UserRepository.UserStatus(ctx, userID)
	if errors.Is(err, repository.ErrUserNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == repository.UserStatusActive, nil
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
	ctx, cancel := context.WithTimeout(r.Context(), databaseRequestTimeout)
	defer cancel()
	active, err := userIsActive(ctx, services, userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, responseEnvelope{Code: 50301, Message: "store data unavailable"})
		return "", false
	}
	if !active {
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
