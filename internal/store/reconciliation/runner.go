package reconciliation

import (
	"context"
	"fmt"
	"time"

	huawei_iap "onebeat/store-api/internal/huawei/iap"
	"onebeat/store-api/internal/store/repository"
)

const (
	DailyJobName           = "trade_reconciliation"
	BackfillJobName        = "trade_backfill"
	ObserveDailyJobName    = "trade_reconciliation_observe"
	ObserveBackfillJobName = "trade_backfill_observe"
	Forward                = "FORWARD"
	Backward               = "BACKWARD"
)

type TradeClient interface {
	QueryTradeOrders(context.Context, time.Time, time.Time, string) (huawei_iap.TradeOrdersPage, error)
}

type Processor interface {
	ProcessTradeOrder(context.Context, huawei_iap.TradeOrder) error
}

type Checkpoints interface {
	AcquireReconciliationLock(context.Context, string, string) (func(context.Context) error, bool, error)
	LoadReconciliationCheckpoint(context.Context, string, string, string) (repository.ReconciliationCheckpoint, error)
	BeginReconciliationWindow(context.Context, string, string, time.Time, time.Time, string, int) error
	SaveReconciliationPage(context.Context, string, string, string, int) error
	CompleteReconciliationWindow(context.Context, string, string, time.Time) error
	FailReconciliationWindow(context.Context, string, string, string) error
}

type Runner struct {
	client      TradeClient
	processor   Processor
	checkpoints Checkpoints
	now         func() time.Time
}

func New(client TradeClient, processor Processor, checkpoints Checkpoints) *Runner {
	return &Runner{client: client, processor: processor, checkpoints: checkpoints, now: time.Now}
}

func (r *Runner) RunWindow(
	ctx context.Context,
	environment string,
	jobName string,
	direction string,
	windowStart time.Time,
	windowEnd time.Time,
) (bool, error) {
	if environment != "PRODUCTION" {
		return false, nil
	}
	if !windowEnd.After(windowStart) || windowEnd.Sub(windowStart) > 48*time.Hour {
		return false, fmt.Errorf("reconciliation window must be greater than zero and at most 48 hours")
	}
	release, acquired, err := r.checkpoints.AcquireReconciliationLock(ctx, environment, jobName)
	if err != nil || !acquired {
		return acquired, err
	}
	defer release(context.Background())

	checkpoint, err := r.checkpoints.LoadReconciliationCheckpoint(ctx, environment, jobName, direction)
	if err != nil {
		return true, err
	}
	token := ""
	pageNumber := 0
	if sameWindow(checkpoint, windowStart, windowEnd) &&
		(checkpoint.Status == "RUNNING" || checkpoint.Status == "FAILED") {
		token = checkpoint.ContinuationToken
		pageNumber = checkpoint.PageNumber
	}
	if err := r.checkpoints.BeginReconciliationWindow(
		ctx, environment, jobName, windowStart, windowEnd, token, pageNumber,
	); err != nil {
		return true, err
	}

	for {
		page, err := r.client.QueryTradeOrders(ctx, windowStart, windowEnd, token)
		if err != nil {
			_ = r.checkpoints.FailReconciliationWindow(ctx, environment, jobName, compactError(err))
			return true, err
		}
		for _, order := range page.Orders {
			if err := r.processor.ProcessTradeOrder(ctx, order); err != nil {
				_ = r.checkpoints.FailReconciliationWindow(ctx, environment, jobName, compactError(err))
				return true, err
			}
		}
		pageNumber++
		if err := r.checkpoints.SaveReconciliationPage(
			ctx, environment, jobName, page.ContinuationToken, pageNumber,
		); err != nil {
			return true, err
		}
		if page.ContinuationToken == "" {
			break
		}
		token = page.ContinuationToken
	}
	if err := r.checkpoints.CompleteReconciliationWindow(
		ctx, environment, jobName, r.now().UTC(),
	); err != nil {
		return true, err
	}
	return true, nil
}

func sameWindow(checkpoint repository.ReconciliationCheckpoint, start time.Time, end time.Time) bool {
	return checkpoint.WindowStart != nil && checkpoint.WindowEnd != nil &&
		checkpoint.WindowStart.Equal(start) && checkpoint.WindowEnd.Equal(end)
}

func compactError(err error) string {
	message := err.Error()
	if len(message) > 500 {
		return message[:500]
	}
	return message
}
