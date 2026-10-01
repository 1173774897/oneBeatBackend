package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"onebeat/store-api/internal/config"
	"onebeat/store-api/internal/database"
	huawei_iap "onebeat/store-api/internal/huawei/iap"
	"onebeat/store-api/internal/store/reconciliation"
	"onebeat/store-api/internal/store/repository"
	storeservice "onebeat/store-api/internal/store/service"
)

const databaseStartupTimeout = 15 * time.Second

func main() {
	environment := envOrDefault("APP_ENV", "development")
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With(
		"component", "iap-reconciler",
		"environment", environment,
	)
	storeConfig, err := config.LoadStoreConfig(environment)
	if err != nil {
		logger.Error("store configuration is invalid", "error", err)
		os.Exit(1)
	}
	if storeConfig.HuaweiIAPEnvironment != "production" {
		logger.Info("trade reconciliation is disabled outside production")
		return
	}

	databaseContext, cancelDatabase := context.WithTimeout(context.Background(), databaseStartupTimeout)
	store, err := database.Open(databaseContext, os.Getenv("DATABASE_URL"))
	cancelDatabase()
	if err != nil {
		logger.Error("database startup check failed", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	huaweiClient, err := huawei_iap.NewClient(&http.Client{Timeout: 20 * time.Second}, huawei_iap.Config{
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
	repo := repository.New(store.Pool())
	storeService := storeservice.New(
		repo,
		huaweiClient,
		storeConfig.PurchaseBindingSecret,
		storeConfig.PurchaseTokenEncryptionKey,
		storeConfig.Environment,
	)
	var processor reconciliation.Processor = storeService
	dailyJobName := reconciliation.DailyJobName
	backfillJobName := reconciliation.BackfillJobName
	if envOrDefault("IAP_RECONCILIATION_OBSERVE_ONLY", "1") == "1" {
		processor = observeOnlyProcessor{logger: logger}
		dailyJobName = reconciliation.ObserveDailyJobName
		backfillJobName = reconciliation.ObserveBackfillJobName
		logger.Info("trade reconciliation is running in observe-only mode")
	}
	runner := reconciliation.New(huaweiClient, processor, repo)

	timezone, err := time.LoadLocation(envOrDefault("IAP_RECONCILIATION_TIMEZONE", "Asia/Shanghai"))
	if err != nil {
		logger.Error("invalid reconciliation timezone", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if os.Getenv("IAP_BACKFILL_ENABLED") == "1" {
		days := envInt("IAP_BACKFILL_DAYS", 180)
		if days > 180 {
			days = 180
		}
		if err := runBackfill(ctx, logger, runner, repo, timezone, days, backfillJobName); err != nil {
			logger.Error("initial trade backfill failed", "error", err)
			os.Exit(1)
		}
	}
	for {
		dailyErr := runDaily(ctx, logger, runner, repo, timezone, dailyJobName)
		if dailyErr != nil {
			logger.Error("daily trade reconciliation failed", "error", dailyErr)
		}
		next := nextRun(time.Now().In(timezone), 2, 15)
		if dailyErr != nil {
			next = time.Now().In(timezone).Add(15 * time.Minute)
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			logger.Info("reconciliation worker stopped")
			return
		case <-timer.C:
		}
	}
}

func runDaily(
	ctx context.Context,
	logger *slog.Logger,
	runner *reconciliation.Runner,
	repo *repository.Repository,
	timezone *time.Location,
	jobName string,
) error {
	today := beginningOfDay(time.Now().In(timezone))
	checkpoint, err := repo.LoadReconciliationCheckpoint(
		ctx, "PRODUCTION", jobName, reconciliation.Forward,
	)
	if err != nil {
		return err
	}
	start := dailyResumeStart(checkpoint, today)
	for start.Before(today) {
		end := start.AddDate(0, 0, 1)
		if end.After(today) {
			end = today
		}
		acquired, err := runner.RunWindow(
			ctx, "PRODUCTION", jobName, reconciliation.Forward, start, end,
		)
		if err != nil {
			return err
		}
		if !acquired {
			logger.Info("daily reconciliation skipped because another worker owns the lock")
			return nil
		}
		start = end
	}
	return nil
}

func runBackfill(
	ctx context.Context,
	logger *slog.Logger,
	runner *reconciliation.Runner,
	repo *repository.Repository,
	timezone *time.Location,
	days int,
	jobName string,
) error {
	today := beginningOfDay(time.Now().In(timezone))
	oldest := today.AddDate(0, 0, -days)
	windowEnd := today
	checkpoint, err := repo.LoadReconciliationCheckpoint(
		ctx, "PRODUCTION", jobName, reconciliation.Backward,
	)
	if err != nil {
		return err
	}
	if checkpoint.WindowStart != nil && checkpoint.WindowEnd != nil {
		if checkpoint.Status == "COMPLETED" {
			windowEnd = checkpoint.WindowStart.In(timezone)
		} else if checkpoint.Status == "FAILED" || checkpoint.Status == "RUNNING" {
			windowEnd = checkpoint.WindowEnd.In(timezone)
		}
	}
	for windowEnd.After(oldest) {
		windowStart := windowEnd.AddDate(0, 0, -1)
		acquired, err := runner.RunWindow(
			ctx, "PRODUCTION", jobName, reconciliation.Backward,
			windowStart, windowEnd,
		)
		if err != nil {
			return err
		}
		if !acquired {
			logger.Info("trade backfill paused because another worker owns the lock")
			return nil
		}
		windowEnd = windowStart
	}
	return nil
}

type observeOnlyProcessor struct {
	logger *slog.Logger
}

func (p observeOnlyProcessor) ProcessTradeOrder(_ context.Context, order huawei_iap.TradeOrder) error {
	p.logger.Info(
		"observed Huawei trade order",
		"tradeType", order.TradeType,
		"productId", order.EffectiveProductID(),
		"purchaseOrderId", order.EffectiveOrderID(),
		"environment", order.Environment,
		"occurredAt", order.OccurredAt(),
	)
	return nil
}

func dailyResumeStart(checkpoint repository.ReconciliationCheckpoint, today time.Time) time.Time {
	if checkpoint.WindowStart == nil || checkpoint.WindowEnd == nil {
		return today.AddDate(0, 0, -1)
	}
	if checkpoint.Status == "FAILED" || checkpoint.Status == "RUNNING" {
		return checkpoint.WindowStart.In(today.Location())
	}
	if checkpoint.Status == "COMPLETED" && checkpoint.WindowEnd.Before(today) {
		return checkpoint.WindowEnd.In(today.Location())
	}
	return today
}

func beginningOfDay(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, value.Location())
}

func nextRun(now time.Time, hour int, minute int) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func envOrDefault(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
