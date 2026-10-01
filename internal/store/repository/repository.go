package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"onebeat/store-api/internal/store/catalog"
)

const providerHuawei = "HUAWEI"

type Repository struct {
	pool *pgxpool.Pool
}

type OrderRecord struct {
	Environment             string
	UserID                  string
	ItemKey                 string
	ProductID               string
	ProductType             string
	OrderID                 string
	OriginalOrderID         *string
	PurchaseTokenHash       []byte
	PurchaseTokenCiphertext []byte
	DeveloperPayload        string
	Status                  string
	PurchasedAt             *time.Time
	ExpiresAt               *time.Time
	Finished                bool
	Receipt                 interface{}
}

type SubscriptionRecord struct {
	Environment             string
	UserID                  string
	ItemKey                 string
	ProductID               string
	SubscriptionKey         string
	PurchaseTokenHash       []byte
	PurchaseTokenCiphertext []byte
	Status                  string
	AutoRenewing            bool
	StartsAt                time.Time
	ExpiresAt               time.Time
	RevokedAt               *time.Time
	VerifiedAt              time.Time
	Source                  string
}

type ApplyResult struct {
	OrderDatabaseID        string
	SubscriptionDatabaseID string
	NeedsConfirm           bool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) UpsertUser(ctx context.Context, unionIDHash []byte, now time.Time) (string, error) {
	var userID string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO users (huawei_union_id_hash, last_login_at)
		VALUES ($1, $2)
		ON CONFLICT (huawei_union_id_hash) DO UPDATE SET
			last_login_at = EXCLUDED.last_login_at,
			updated_at = EXCLUDED.last_login_at
		WHERE users.status = 'ACTIVE'
		RETURNING id::text
	`, unionIDHash, now).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("user account is not active")
	}
	if err != nil {
		return "", fmt.Errorf("upsert user: %w", err)
	}
	return userID, nil
}

func (r *Repository) BindDeveloperPayload(ctx context.Context, userID string, developerPayload string, now time.Time) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE users
		SET developer_payload = $2, updated_at = $3
		WHERE id = $1 AND (developer_payload IS NULL OR developer_payload = $2)
	`, userID, developerPayload, now)
	if err != nil {
		return fmt.Errorf("bind purchase developer payload: %w", err)
	}
	if result.RowsAffected() != 1 {
		return errors.New("purchase developer payload is already bound differently")
	}
	return nil
}

func (r *Repository) Bootstrap(
	ctx context.Context,
	userID string,
	developerPayload string,
	now time.Time,
) (catalog.Bootstrap, error) {
	grants := make(map[string]catalog.Access)
	rows, err := r.pool.Query(ctx, `
		SELECT entitlement_key, source_type, ends_at
		FROM entitlement_grants
		WHERE user_id = $1
		  AND revoked_at IS NULL
		  AND starts_at <= $2
		  AND (ends_at IS NULL OR ends_at > $2)
	`, userID, now)
	if err != nil {
		return catalog.Bootstrap{}, fmt.Errorf("query entitlement grants: %w", err)
	}
	defer rows.Close()

	pass := catalog.PassSnapshot{}
	for rows.Next() {
		var itemKey string
		var sourceType string
		var endsAt *time.Time
		if err := rows.Scan(&itemKey, &sourceType, &endsAt); err != nil {
			return catalog.Bootstrap{}, fmt.Errorf("scan entitlement grant: %w", err)
		}
		reason := catalog.AccessIAPPurchase
		if sourceType == "REDEMPTION" {
			reason = catalog.AccessRedemption
		}
		grants[itemKey] = catalog.Access{Allowed: true, Reason: reason, ValidUntil: endsAt}
		if itemKey == "pass.all" {
			pass.Active = true
			pass.Source = reason
			pass.ExpiresAt = endsAt
		}
	}
	if err := rows.Err(); err != nil {
		return catalog.Bootstrap{}, fmt.Errorf("iterate entitlement grants: %w", err)
	}

	var status string
	var expiresAt time.Time
	var autoRenewing bool
	err = r.pool.QueryRow(ctx, `
		SELECT status, expires_at, auto_renewing
		FROM iap_subscriptions
		WHERE user_id = $1
		ORDER BY verified_at DESC
		LIMIT 1
	`, userID).Scan(&status, &expiresAt, &autoRenewing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return catalog.Bootstrap{}, fmt.Errorf("query pass subscription: %w", err)
	}
	if err == nil {
		pass.Status = status
		pass.ExpiresAt = &expiresAt
		pass.AutoRenewing = autoRenewing
		if pass.Active && pass.Source == catalog.AccessIAPPurchase {
			pass.Source = catalog.AccessIAPPass
		}
	}

	return catalog.AuthenticatedBootstrap(now, developerPayload, grants, pass), nil
}

func (r *Repository) ApplyNonConsumable(ctx context.Context, record OrderRecord, now time.Time) (ApplyResult, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ApplyResult{}, err
	}
	defer tx.Rollback(ctx)

	transactionID, alreadyAcknowledged, _, err := upsertPurchaseTransaction(ctx, tx, record, nil, "INITIAL", now)
	if err != nil {
		return ApplyResult{}, err
	}
	if record.Status == "PURCHASED" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO entitlement_grants
				(user_id, entitlement_key, source_type, source_ref, starts_at)
			VALUES ($1, $2, 'IAP_NONCONSUMABLE', $3, $4)
			ON CONFLICT (source_type, source_ref, entitlement_key) DO UPDATE SET
				revoked_at = NULL,
				revoke_reason = NULL,
				updated_at = EXCLUDED.starts_at
		`, record.UserID, record.ItemKey, transactionID, now); err != nil {
			return ApplyResult{}, fmt.Errorf("grant non-consumable entitlement: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE entitlement_grants SET revoked_at = $2, revoke_reason = $3, updated_at = $2
			WHERE source_type = 'IAP_NONCONSUMABLE' AND source_ref = $1 AND revoked_at IS NULL
		`, transactionID, now, record.Status); err != nil {
			return ApplyResult{}, fmt.Errorf("revoke non-consumable entitlement: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{
		OrderDatabaseID: transactionID,
		NeedsConfirm:    record.Status == "PURCHASED" && !record.Finished && !alreadyAcknowledged,
	}, nil
}

func (r *Repository) ApplySubscription(
	ctx context.Context,
	order OrderRecord,
	subscription SubscriptionRecord,
	now time.Time,
) (ApplyResult, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ApplyResult{}, err
	}
	defer tx.Rollback(ctx)

	subscriptionID, err := resolveSubscription(ctx, tx, subscription, order.DeveloperPayload, now)
	if err != nil {
		return ApplyResult{}, err
	}
	tokenID, err := upsertSubscriptionToken(ctx, tx, subscriptionID, subscription, now)
	if err != nil {
		return ApplyResult{}, err
	}

	var existingPurchaseCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM iap_provider_transactions
		WHERE subscription_id = $1 AND trade_type = 'PURCHASE'
	`, subscriptionID).Scan(&existingPurchaseCount); err != nil {
		return ApplyResult{}, fmt.Errorf("count subscription purchases: %w", err)
	}
	subtype := "RENEWAL"
	if existingPurchaseCount == 0 {
		subtype = "INITIAL"
	}
	transactionID, alreadyAcknowledged, _, err := upsertPurchaseTransaction(
		ctx, tx, order, &subscriptionID, subtype, now,
	)
	if err != nil {
		return ApplyResult{}, err
	}

	periodStatus := "PAID"
	if subscription.Status == "EXPIRED" || !subscription.ExpiresAt.After(now) {
		periodStatus = "EXPIRED"
	}
	if subscription.Status == "REVOKED" {
		periodStatus = "REVOKED"
	}
	var periodID string
	err = tx.QueryRow(ctx, `
		INSERT INTO iap_subscription_periods
			(provider, environment, subscription_id, purchase_transaction_id, purchase_token_id,
			 purchase_order_id, starts_at, expires_at, status)
		VALUES ('HUAWEI', $1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (provider, environment, purchase_order_id) DO UPDATE SET
			status = EXCLUDED.status,
			expires_at = EXCLUDED.expires_at,
			updated_at = $9
		WHERE iap_subscription_periods.subscription_id = EXCLUDED.subscription_id
		RETURNING id::text
	`, subscription.Environment, subscriptionID, transactionID, tokenID, order.OrderID,
		subscription.StartsAt, subscription.ExpiresAt, periodStatus, now).Scan(&periodID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ApplyResult{}, errors.New("subscription period is already bound to another subscription")
	}
	if err != nil {
		return ApplyResult{}, fmt.Errorf("upsert subscription period: %w", err)
	}

	_, err = tx.Exec(ctx, `
		UPDATE iap_subscriptions SET
			latest_period_id = $2,
			latest_purchase_order_id = $3,
			status = $4,
			auto_renewing = $5,
			starts_at = LEAST(starts_at, $6),
			expires_at = $7,
			revoked_at = $8,
			verified_at = $9,
			version = version + 1,
			updated_at = $9
		WHERE id = $1
	`, subscriptionID, periodID, order.OrderID, subscription.Status, subscription.AutoRenewing,
		subscription.StartsAt, subscription.ExpiresAt, subscription.RevokedAt,
		subscription.VerifiedAt)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("update subscription snapshot: %w", err)
	}

	active := subscription.ExpiresAt.After(now) &&
		(subscription.Status == "ACTIVE" || subscription.Status == "CANCELED_ACTIVE")
	if active {
		if _, err := tx.Exec(ctx, `
			INSERT INTO entitlement_grants
				(user_id, entitlement_key, source_type, source_ref, starts_at, ends_at)
			VALUES ($1, 'pass.all', 'IAP_SUBSCRIPTION', $2, $3, $4)
			ON CONFLICT (source_type, source_ref, entitlement_key) DO UPDATE SET
				starts_at = LEAST(entitlement_grants.starts_at, EXCLUDED.starts_at),
				ends_at = EXCLUDED.ends_at,
				revoked_at = NULL,
				revoke_reason = NULL,
				updated_at = $5
		`, subscription.UserID, subscriptionID, subscription.StartsAt, subscription.ExpiresAt, now); err != nil {
			return ApplyResult{}, fmt.Errorf("grant subscription entitlement: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE entitlement_grants SET revoked_at = $2, revoke_reason = $3, updated_at = $2
			WHERE source_type = 'IAP_SUBSCRIPTION' AND source_ref = $1 AND revoked_at IS NULL
		`, subscriptionID, now, subscription.Status); err != nil {
			return ApplyResult{}, fmt.Errorf("revoke subscription entitlement: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{
		OrderDatabaseID:        transactionID,
		SubscriptionDatabaseID: subscriptionID,
		NeedsConfirm:           active && !order.Finished && !alreadyAcknowledged,
	}, nil
}

func resolveSubscription(
	ctx context.Context,
	tx pgx.Tx,
	record SubscriptionRecord,
	developerPayload string,
	now time.Time,
) (string, error) {
	var subscriptionID string
	err := tx.QueryRow(ctx, `
		SELECT s.id::text
		FROM iap_subscription_tokens t
		JOIN iap_subscriptions s ON s.id = t.subscription_id
		WHERE t.provider = 'HUAWEI' AND t.environment = $1 AND t.purchase_token_hash = $2
		  AND s.user_id = $3 AND s.huawei_product_id = $4
		FOR UPDATE OF s
	`, record.Environment, record.PurchaseTokenHash, record.UserID, record.ProductID).Scan(&subscriptionID)
	if err == nil {
		return subscriptionID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("find subscription by token: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT id::text
		FROM iap_subscriptions
		WHERE provider = 'HUAWEI' AND environment = $1 AND user_id = $2
		  AND huawei_product_id = $3 AND status <> 'REVOKED'
		ORDER BY verified_at DESC
		FOR UPDATE
	`, record.Environment, record.UserID, record.ProductID)
	if err != nil {
		return "", fmt.Errorf("find active subscription: %w", err)
	}
	defer rows.Close()
	activeIDs := make([]string, 0, 2)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		activeIDs = append(activeIDs, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(activeIDs) == 1 {
		return activeIDs[0], nil
	}
	if len(activeIDs) > 1 {
		return "", errors.New("multiple active subscriptions require manual reconciliation")
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO iap_subscriptions
			(provider, environment, user_id, item_key, huawei_product_id, subscription_key,
			 developer_payload, status, auto_renewing, starts_at, expires_at,
			 revoked_at, verified_at, created_at, updated_at)
		VALUES ('HUAWEI', $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)
		ON CONFLICT (provider, environment, user_id, huawei_product_id, subscription_key) DO UPDATE SET
			verified_at = EXCLUDED.verified_at,
			updated_at = EXCLUDED.updated_at
		RETURNING id::text
	`, record.Environment, record.UserID, record.ItemKey, record.ProductID, record.SubscriptionKey,
		developerPayload, record.Status, record.AutoRenewing, record.StartsAt, record.ExpiresAt,
		record.RevokedAt, record.VerifiedAt, now).Scan(&subscriptionID)
	if err != nil {
		return "", fmt.Errorf("create subscription: %w", err)
	}
	return subscriptionID, nil
}

func upsertSubscriptionToken(
	ctx context.Context,
	tx pgx.Tx,
	subscriptionID string,
	record SubscriptionRecord,
	now time.Time,
) (string, error) {
	source := record.Source
	if source == "" {
		source = "CLIENT_RESTORE"
	}
	var tokenID string
	err := tx.QueryRow(ctx, `
		INSERT INTO iap_subscription_tokens
			(provider, environment, subscription_id, purchase_token_hash,
			 purchase_token_ciphertext, first_seen_at, last_seen_at, source)
		VALUES ('HUAWEI', $1, $2, $3, $4, $5, $5, $6)
		ON CONFLICT (provider, environment, purchase_token_hash) DO UPDATE SET
			last_seen_at = EXCLUDED.last_seen_at,
			purchase_token_ciphertext = EXCLUDED.purchase_token_ciphertext,
			source = EXCLUDED.source
		WHERE iap_subscription_tokens.subscription_id = EXCLUDED.subscription_id
		RETURNING id::text
	`, record.Environment, subscriptionID, record.PurchaseTokenHash,
		record.PurchaseTokenCiphertext, now, source).Scan(&tokenID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("purchase token is already bound to another subscription")
	}
	if err != nil {
		return "", fmt.Errorf("upsert subscription token: %w", err)
	}
	return tokenID, nil
}

func upsertPurchaseTransaction(
	ctx context.Context,
	tx pgx.Tx,
	record OrderRecord,
	subscriptionID *string,
	subtype string,
	now time.Time,
) (string, bool, bool, error) {
	payload, err := json.Marshal(record.Receipt)
	if err != nil {
		return "", false, false, fmt.Errorf("encode transaction snapshot: %w", err)
	}
	redactedPayload := redactPurchaseTokens(payload)
	occurredAt := now
	if record.PurchasedAt != nil && !record.PurchasedAt.IsZero() {
		occurredAt = record.PurchasedAt.UTC()
	}
	var transactionID string
	var acknowledgedAt *time.Time
	var inserted bool
	err = tx.QueryRow(ctx, `
		INSERT INTO iap_provider_transactions
			(provider, environment, user_id, subscription_id, item_key, huawei_product_id,
			 product_type, purchase_order_id, original_purchase_order_id, purchase_token_hash,
			 purchase_token_ciphertext, developer_payload, trade_type, transaction_subtype,
			 entitlement_effect, provider_status, occurred_at, acknowledged_at, verified_at, payload_snapshot)
		VALUES ('HUAWEI', $1, $2, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11,
			'PURCHASE', $12, 'NONE', $13, $14,
			(CASE WHEN $15::boolean THEN $16::timestamptz END), $16, $17::jsonb)
		ON CONFLICT (provider, environment, purchase_order_id, trade_type) DO UPDATE SET
			purchase_token_hash = EXCLUDED.purchase_token_hash,
			purchase_token_ciphertext = EXCLUDED.purchase_token_ciphertext,
			provider_status = EXCLUDED.provider_status,
			verified_at = EXCLUDED.verified_at,
			payload_snapshot = EXCLUDED.payload_snapshot,
			updated_at = EXCLUDED.verified_at,
			acknowledged_at = COALESCE(iap_provider_transactions.acknowledged_at, EXCLUDED.acknowledged_at)
		WHERE iap_provider_transactions.user_id = EXCLUDED.user_id
		  AND iap_provider_transactions.huawei_product_id = EXCLUDED.huawei_product_id
		RETURNING id::text, acknowledged_at, (xmax = 0)
	`, record.Environment, record.UserID, subscriptionID, record.ItemKey, record.ProductID,
		record.ProductType, record.OrderID, record.OriginalOrderID, record.PurchaseTokenHash,
		record.PurchaseTokenCiphertext, record.DeveloperPayload, subtype, record.Status,
		occurredAt, record.Finished, now, redactedPayload).Scan(&transactionID, &acknowledgedAt, &inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, false, errors.New("transaction is already bound to another OneBeat account or product")
	}
	if err != nil {
		return "", false, false, fmt.Errorf("upsert provider transaction: %w", err)
	}
	return transactionID, acknowledgedAt != nil, inserted, nil
}

func (r *Repository) MarkOrderAcknowledged(ctx context.Context, transactionID string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE iap_provider_transactions
		SET acknowledged_at = COALESCE(acknowledged_at, $2), updated_at = $2
		WHERE id = $1
	`, transactionID, now)
	return err
}

func redactPurchaseTokens(payload []byte) []byte {
	var value interface{}
	if json.Unmarshal(payload, &value) != nil {
		return []byte(`{}`)
	}
	redactJSONTokens(value)
	redacted, err := json.Marshal(value)
	if err != nil {
		return []byte(`{}`)
	}
	return redacted
}

func redactJSONTokens(value interface{}) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, nested := range typed {
			if key == "purchaseToken" {
				typed[key] = "[redacted]"
				continue
			}
			redactJSONTokens(nested)
		}
	case []interface{}:
		for _, nested := range typed {
			redactJSONTokens(nested)
		}
	}
}
