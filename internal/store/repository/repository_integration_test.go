package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSubscriptionRenewalIsIdempotentAndKeepsPriorPeriod(t *testing.T) {
	databaseURL := os.Getenv("ONEBEAT_INTEGRATION_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ONEBEAT_INTEGRATION_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var userID string
	now := time.Now().UTC().Truncate(time.Second)
	err = pool.QueryRow(ctx, `
		INSERT INTO users (huawei_union_id_hash, developer_payload, last_login_at)
		VALUES ($1, $2, $3)
		RETURNING id::text
	`, []byte("integration-user"), "binding", now).Scan(&userID)
	if err != nil {
		t.Fatal(err)
	}

	repo := New(pool)
	apply := func(orderID string, tokenByte byte, startsAt time.Time, expiresAt time.Time) {
		t.Helper()
		tokenHash := []byte{tokenByte}
		result, err := repo.ApplySubscription(ctx, OrderRecord{
			Environment: "SANDBOX", UserID: userID, ItemKey: "pass.all",
			ProductID: "onebeat.pass.monthly", ProductType: "AUTORENEWABLE",
			OrderID: orderID, PurchaseTokenHash: tokenHash,
			PurchaseTokenCiphertext: []byte{tokenByte, tokenByte},
			DeveloperPayload:        "binding", Status: "PURCHASED", PurchasedAt: &startsAt,
			ExpiresAt: &expiresAt, Receipt: map[string]string{"purchaseToken": "must-redact"},
		}, SubscriptionRecord{
			Environment: "SANDBOX", UserID: userID, ItemKey: "pass.all",
			ProductID: "onebeat.pass.monthly", SubscriptionKey: "logical-subscription",
			PurchaseTokenHash: tokenHash, PurchaseTokenCiphertext: []byte{tokenByte, tokenByte},
			Status: "ACTIVE", AutoRenewing: true, StartsAt: startsAt, ExpiresAt: expiresAt,
			VerifiedAt: now, Source: "CLIENT_RESTORE",
		}, now)
		if err != nil {
			t.Fatalf("ApplySubscription(%s): %v", orderID, err)
		}
		if result.OrderDatabaseID == "" || result.SubscriptionDatabaseID == "" {
			t.Fatalf("ApplySubscription(%s) returned empty IDs", orderID)
		}
	}

	firstStart := now.Add(-time.Hour)
	firstEnd := now.Add(29 * 24 * time.Hour)
	apply("order-1", 1, firstStart, firstEnd)
	apply("order-1", 1, firstStart, firstEnd)
	apply("order-2", 2, firstEnd, firstEnd.Add(30*24*time.Hour))

	var transactions int
	var periods int
	var tokens int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM iap_provider_transactions`).Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM iap_subscription_periods`).Scan(&periods); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM iap_subscription_tokens`).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if transactions != 2 || periods != 2 || tokens != 2 {
		t.Fatalf("transactions=%d periods=%d tokens=%d", transactions, periods, tokens)
	}

	var revokedPurchases int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM iap_provider_transactions
		WHERE trade_type = 'PURCHASE' AND provider_status IN ('REFUNDED', 'REVOKED')
	`).Scan(&revokedPurchases); err != nil {
		t.Fatal(err)
	}
	if revokedPurchases != 0 {
		t.Fatalf("renewal revoked %d previous purchases", revokedPurchases)
	}

	var subscriptionID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM iap_subscriptions`).Scan(&subscriptionID); err != nil {
		t.Fatal(err)
	}
	refund := RefundRecord{
		Environment: "SANDBOX", UserID: userID, SubscriptionID: &subscriptionID,
		ItemKey: "pass.all", ProductID: "onebeat.pass.monthly", ProductType: "AUTORENEWABLE",
		OrderID: "refund-1", PurchaseTokenHash: []byte{2},
		PurchaseTokenCiphertext: []byte{2, 2}, DeveloperPayload: "binding",
		RefundType: "WITHDRAWAL", EntitlementEffect: "REVOKE",
		ProviderStatus: "REVOKE", OccurredAt: now, Receipt: map[string]string{"purchaseToken": "must-redact"},
	}
	if err := repo.ApplyRefund(ctx, refund, now); err != nil {
		t.Fatalf("ApplyRefund(): %v", err)
	}
	if err := repo.ApplyRefund(ctx, refund, now); err != nil {
		t.Fatalf("idempotent ApplyRefund(): %v", err)
	}
	var refundCount int
	var subscriptionStatus string
	var activeGrantCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM iap_provider_transactions WHERE trade_type = 'REFUND'
	`).Scan(&refundCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM iap_subscriptions WHERE id = $1`, subscriptionID).Scan(&subscriptionStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM entitlement_grants
		WHERE source_type = 'IAP_SUBSCRIPTION' AND source_ref = $1 AND revoked_at IS NULL
	`, subscriptionID).Scan(&activeGrantCount); err != nil {
		t.Fatal(err)
	}
	if refundCount != 1 || subscriptionStatus != "REVOKED" || activeGrantCount != 0 {
		t.Fatalf(
			"refundCount=%d subscriptionStatus=%s activeGrantCount=%d",
			refundCount,
			subscriptionStatus,
			activeGrantCount,
		)
	}

	nonConsumableToken := []byte{3}
	result, err := repo.ApplyNonConsumable(ctx, OrderRecord{
		Environment: "SANDBOX", UserID: userID, ItemKey: "character.cloud_walker",
		ProductID: "onebeat.character.cloud", ProductType: "NONCONSUMABLE",
		OrderID: "order-character-1", PurchaseTokenHash: nonConsumableToken,
		PurchaseTokenCiphertext: []byte{3, 3}, DeveloperPayload: "binding",
		Status: "PURCHASED", PurchasedAt: &now, Finished: true,
		Receipt: map[string]string{"purchaseToken": "must-redact"},
	}, now)
	if err != nil {
		t.Fatalf("ApplyNonConsumable(): %v", err)
	}
	nonConsumableRefund := RefundRecord{
		Environment: "SANDBOX", UserID: userID, ItemKey: "character.cloud_walker",
		ProductID: "onebeat.character.cloud", ProductType: "NONCONSUMABLE",
		OrderID: "refund-character-1", PurchaseTokenHash: nonConsumableToken,
		PurchaseTokenCiphertext: []byte{3, 3}, DeveloperPayload: "binding",
		RefundType: "USER_REFUND", EntitlementEffect: "REVOKE",
		ProviderStatus: "REFUND_TRANSACTION", OccurredAt: now,
		Receipt: map[string]string{"purchaseToken": "must-redact"},
	}
	if err := repo.ApplyRefund(ctx, nonConsumableRefund, now); err != nil {
		t.Fatalf("ApplyRefund(non-consumable): %v", err)
	}
	nonConsumableRefund.EntitlementEffect = "PENDING"
	if err := repo.ApplyRefund(ctx, nonConsumableRefund, now.Add(time.Minute)); err != nil {
		t.Fatalf("ApplyRefund(non-consumable retry): %v", err)
	}
	var nonConsumableEffect string
	if err := pool.QueryRow(ctx, `
		SELECT entitlement_effect FROM iap_provider_transactions
		WHERE purchase_order_id = 'refund-character-1' AND trade_type = 'REFUND'
	`).Scan(&nonConsumableEffect); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM entitlement_grants
		WHERE source_type = 'IAP_NONCONSUMABLE' AND source_ref = $1
		  AND revoked_at IS NULL
	`, result.OrderDatabaseID).Scan(&activeGrantCount); err != nil {
		t.Fatal(err)
	}
	if nonConsumableEffect != "REVOKE" || activeGrantCount != 0 {
		t.Fatalf(
			"nonConsumableEffect=%s activeGrantCount=%d",
			nonConsumableEffect,
			activeGrantCount,
		)
	}
}
