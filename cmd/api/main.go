package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"onebeat/store-api/internal/auth"
	"onebeat/store-api/internal/config"
	"onebeat/store-api/internal/database"
	"onebeat/store-api/internal/httpapi"
	huawei_iap "onebeat/store-api/internal/huawei/iap"
	"onebeat/store-api/internal/store/catalog"
	"onebeat/store-api/internal/store/redemption"
	"onebeat/store-api/internal/store/repository"
	storeservice "onebeat/store-api/internal/store/service"
)

const (
	databaseStartupTimeout = 15 * time.Second
	shutdownTimeout        = 10 * time.Second
)

func main() {
	environment := envOrDefault("APP_ENV", "development")
	version := envOrDefault("APP_VERSION", "dev")
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With(
		"environment", environment,
		"version", version,
	)
	if err := catalog.Validate(); err != nil {
		logger.Error("store catalog validation failed", "error", err)
		os.Exit(1)
	}

	databaseContext, cancelDatabase := context.WithTimeout(context.Background(), databaseStartupTimeout)
	store, err := database.Open(databaseContext, os.Getenv("DATABASE_URL"))
	cancelDatabase()
	if err != nil {
		logger.Error("database startup check failed", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	logger.Info("database connection established")

	storeConfig, err := config.LoadStoreConfig(environment)
	if err != nil {
		logger.Error("store configuration is invalid", "error", err)
		os.Exit(1)
	}
	// Campaigns are compiled into the binary, while code digests stay in the
	// secret mount. Refuse startup unless both sides agree exactly.
	redemptionCodes, err := redemption.LoadCodeBook(
		storeConfig.RedemptionCodesPath,
		storeConfig.RedemptionCodePepper,
		catalog.RequiredRedemptionCodeKeys(),
	)
	if err != nil {
		logger.Error("redemption code configuration is invalid", "error", err)
		os.Exit(1)
	}
	huaweiHTTPClient := &http.Client{Timeout: 10 * time.Second}
	huaweiIdentityVerifier := auth.NewHuaweiIDVerifier(huaweiHTTPClient, storeConfig.HuaweiClientID)
	huaweiIdentityAuthenticator := auth.NewHuaweiAccountAuthenticator(
		huaweiHTTPClient,
		storeConfig.HuaweiClientID,
		storeConfig.HuaweiAccountClientSecret,
		huaweiIdentityVerifier,
	)
	huaweiIAPClient, err := huawei_iap.NewClient(huaweiHTTPClient, huawei_iap.Config{
		PrivateKeyPEM: storeConfig.HuaweiIAPPrivateKey,
		KeyID:         storeConfig.HuaweiIAPKeyID,
		IssuerID:      storeConfig.HuaweiIAPIssuerID,
		Environment:   storeConfig.HuaweiIAPEnvironment,
		ApplicationID: config.HuaweiApplicationID,
		PackageName:   config.HuaweiPackageName,
	})
	if err != nil {
		logger.Error("Huawei IAP configuration is invalid", "error", err)
		os.Exit(1)
	}
	storeRepository := repository.New(store.Pool())
	storeService := storeservice.New(
		storeRepository,
		huaweiIAPClient,
		storeConfig.PurchaseBindingSecret,
		storeConfig.PurchaseTokenEncryptionKey,
	)
	storeService.ConfigureRedemptions(redemptionCodes)
	storeServices := &httpapi.StoreServices{
		HuaweiIdentityAuthenticator: huaweiIdentityAuthenticator,
		UserRepository:              storeRepository,
		SessionManager:              auth.NewSessionManager(storeConfig.JWTSigningKey, storeConfig.SessionTTL),
		StoreService:                storeService,
		AccountUnionIDPepper:        storeConfig.AccountUnionIDPepper,
	}

	server := &http.Server{
		Addr:              envOrDefault("STORE_API_ADDR", ":8443"),
		Handler:           httpapi.NewHandlerWithStoreServices(logger, environment, version, store, storeServices),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErrors := make(chan error, 1)
	go func() {
		if os.Getenv("STORE_API_INSECURE_HTTP") == "1" {
			logger.Warn("store API is using insecure HTTP", "address", server.Addr)
			serveErrors <- server.ListenAndServe()
			return
		}

		certFile := envOrDefault("STORE_API_TLS_CERT", "certs/dev-cert.pem")
		keyFile := envOrDefault("STORE_API_TLS_KEY", "certs/dev-key.pem")
		logger.Info("store API is listening", "address", server.Addr, "scheme", "https")
		serveErrors <- server.ListenAndServeTLS(certFile, keyFile)
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case signalValue := <-stop:
		logger.Info("shutdown signal received", "signal", signalValue.String())
	case err := <-serveErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("store API stopped unexpectedly", "error", err)
			os.Exit(1)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("store API stopped")
}

func envOrDefault(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
