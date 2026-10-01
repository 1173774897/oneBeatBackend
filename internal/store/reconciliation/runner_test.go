package reconciliation

import (
	"context"
	"errors"
	"testing"
	"time"

	huawei_iap "onebeat/store-api/internal/huawei/iap"
	"onebeat/store-api/internal/store/repository"
)

type fakeTradeClient struct {
	pages map[string]huawei_iap.TradeOrdersPage
	fail  map[string]error
	seen  []string
}

func (f *fakeTradeClient) QueryTradeOrders(
	_ context.Context,
	_ time.Time,
	_ time.Time,
	token string,
) (huawei_iap.TradeOrdersPage, error) {
	f.seen = append(f.seen, token)
	if err := f.fail[token]; err != nil {
		return huawei_iap.TradeOrdersPage{}, err
	}
	return f.pages[token], nil
}

type fakeProcessor struct {
	orders []string
}

func (f *fakeProcessor) ProcessTradeOrder(_ context.Context, order huawei_iap.TradeOrder) error {
	f.orders = append(f.orders, order.EffectiveOrderID())
	return nil
}

type fakeCheckpoints struct {
	checkpoint  repository.ReconciliationCheckpoint
	savedTokens []string
	completed   int
	failed      int
}

func (f *fakeCheckpoints) AcquireReconciliationLock(
	context.Context,
	string,
	string,
) (func(context.Context) error, bool, error) {
	return func(context.Context) error { return nil }, true, nil
}

func (f *fakeCheckpoints) LoadReconciliationCheckpoint(
	_ context.Context,
	environment string,
	jobName string,
	direction string,
) (repository.ReconciliationCheckpoint, error) {
	checkpoint := f.checkpoint
	checkpoint.Environment = environment
	checkpoint.JobName = jobName
	checkpoint.Direction = direction
	return checkpoint, nil
}

func (f *fakeCheckpoints) BeginReconciliationWindow(
	_ context.Context,
	_ string,
	_ string,
	start time.Time,
	end time.Time,
	token string,
	page int,
) error {
	f.checkpoint.WindowStart = &start
	f.checkpoint.WindowEnd = &end
	f.checkpoint.ContinuationToken = token
	f.checkpoint.PageNumber = page
	f.checkpoint.Status = "RUNNING"
	return nil
}

func (f *fakeCheckpoints) SaveReconciliationPage(
	_ context.Context,
	_ string,
	_ string,
	token string,
	page int,
) error {
	f.savedTokens = append(f.savedTokens, token)
	f.checkpoint.ContinuationToken = token
	f.checkpoint.PageNumber = page
	return nil
}

func (f *fakeCheckpoints) CompleteReconciliationWindow(
	context.Context,
	string,
	string,
	time.Time,
) error {
	f.completed++
	return nil
}

func (f *fakeCheckpoints) FailReconciliationWindow(
	context.Context,
	string,
	string,
	string,
) error {
	f.failed++
	f.checkpoint.Status = "FAILED"
	return nil
}

func TestRunnerReadsEveryContinuationPage(t *testing.T) {
	client := &fakeTradeClient{pages: map[string]huawei_iap.TradeOrdersPage{
		"": {
			Orders:            []huawei_iap.TradeOrder{{PurchaseOrderID: "O1"}},
			ContinuationToken: "next",
		},
		"next": {Orders: []huawei_iap.TradeOrder{{PurchaseOrderID: "O2"}}},
	}}
	processor := &fakeProcessor{}
	checkpoints := &fakeCheckpoints{}
	runner := New(client, processor, checkpoints)
	start := time.Unix(1_700_000_000, 0).UTC()
	acquired, err := runner.RunWindow(
		context.Background(), "PRODUCTION", DailyJobName, Forward, start, start.Add(24*time.Hour),
	)
	if err != nil || !acquired {
		t.Fatalf("RunWindow() acquired=%v err=%v", acquired, err)
	}
	if len(client.seen) != 2 || client.seen[0] != "" || client.seen[1] != "next" {
		t.Fatalf("tokens = %#v", client.seen)
	}
	if len(processor.orders) != 2 || checkpoints.completed != 1 {
		t.Fatalf("orders=%#v completed=%d", processor.orders, checkpoints.completed)
	}
}

func TestRunnerKeepsCheckpointAtLastSuccessfulPage(t *testing.T) {
	client := &fakeTradeClient{
		pages: map[string]huawei_iap.TradeOrdersPage{
			"": {ContinuationToken: "next"},
		},
		fail: map[string]error{"next": errors.New("second page failed")},
	}
	checkpoints := &fakeCheckpoints{}
	runner := New(client, &fakeProcessor{}, checkpoints)
	start := time.Unix(1_700_000_000, 0).UTC()
	_, err := runner.RunWindow(
		context.Background(), "PRODUCTION", DailyJobName, Forward, start, start.Add(24*time.Hour),
	)
	if err == nil {
		t.Fatal("expected second page failure")
	}
	if checkpoints.checkpoint.ContinuationToken != "next" || checkpoints.checkpoint.PageNumber != 1 {
		t.Fatalf("checkpoint = %#v", checkpoints.checkpoint)
	}
	if checkpoints.completed != 0 || checkpoints.failed != 1 {
		t.Fatalf("completed=%d failed=%d", checkpoints.completed, checkpoints.failed)
	}
}

func TestRunnerSkipsSandboxWithoutCallingHuawei(t *testing.T) {
	client := &fakeTradeClient{}
	runner := New(client, &fakeProcessor{}, &fakeCheckpoints{})
	start := time.Unix(1_700_000_000, 0).UTC()
	acquired, err := runner.RunWindow(
		context.Background(), "SANDBOX", DailyJobName, Forward, start, start.Add(24*time.Hour),
	)
	if err != nil || acquired || len(client.seen) != 0 {
		t.Fatalf("acquired=%v err=%v seen=%#v", acquired, err, client.seen)
	}
}
