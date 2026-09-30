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

type Repository struct {
	pool *pgxpool.Pool
}

type OrderRecord struct {
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
	VerifiedAt              time.Time
}

type ApplyResult struct {
	OrderDatabaseID string
	NeedsConfirm    bool
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
		access := catalog.Access{Allowed: true, Reason: reason, ValidUntil: endsAt}
		grants[itemKey] = access
		if itemKey == "pass.all" {
			pass.Active = true
			pass.Source = reason
			pass.ExpiresAt = endsAt
		}
	}
	if err := rows.Err(); err != nil {
		return catalog.Bootstrap{}, fmt.Errorf("iterate entitlement grants: %w", err)
	}
	if pass.Active && pass.Source == catalog.AccessIAPPurchase {
		pass.Source = catalog.AccessIAPPass
		var autoRenewing bool
		_ = r.pool.QueryRow(ctx, `
			SELECT auto_renewing
			FROM iap_subscriptions
			WHERE user_id = $1 AND expires_at > $2 AND status IN ('ACTIVE', 'CANCELED_ACTIVE')
			ORDER BY expires_at DESC
			LIMIT 1
		`, userID, now).Scan(&autoRenewing)
		pass.AutoRenewing = autoRenewing
	}
	return catalog.AuthenticatedBootstrap(now, developerPayload, grants, pass), nil
}

func (r *Repository) ApplyNonConsumable(ctx context.Context, record OrderRecord, now time.Time) (ApplyResult, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ApplyResult{}, err
	}
	defer tx.Rollback(ctx)

	orderDatabaseID, alreadyAcknowledged, err := upsertOrder(ctx, tx, record, now)
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
		`, record.UserID, record.ItemKey, orderDatabaseID, now); err != nil {
			return ApplyResult{}, fmt.Errorf("grant non-consumable entitlement: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE entitlement_grants SET revoked_at = $2, revoke_reason = $3, updated_at = $2
			WHERE source_type = 'IAP_NONCONSUMABLE' AND source_ref = $1 AND revoked_at IS NULL
		`, orderDatabaseID, now, record.Status); err != nil {
			return ApplyResult{}, fmt.Errorf("revoke non-consumable entitlement: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{OrderDatabaseID: orderDatabaseID, NeedsConfirm: record.Status == "PURCHASED" && !record.Finished && !alreadyAcknowledged}, nil
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

	orderDatabaseID, alreadyAcknowledged, err := upsertOrder(ctx, tx, order, now)
	if err != nil {
		return ApplyResult{}, err
	}
	var subscriptionID string
	err = tx.QueryRow(ctx, `
		INSERT INTO iap_subscriptions
			(user_id, item_key, huawei_product_id, subscription_key, latest_order_id,
			 purchase_token_hash, purchase_token_ciphertext, status, auto_renewing,
			 starts_at, expires_at, verified_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (subscription_key) DO UPDATE SET
			latest_order_id = EXCLUDED.latest_order_id,
			purchase_token_hash = EXCLUDED.purchase_token_hash,
			purchase_token_ciphertext = EXCLUDED.purchase_token_ciphertext,
			status = EXCLUDED.status,
			auto_renewing = EXCLUDED.auto_renewing,
			starts_at = LEAST(iap_subscriptions.starts_at, EXCLUDED.starts_at),
			expires_at = EXCLUDED.expires_at,
			verified_at = EXCLUDED.verified_at,
			updated_at = EXCLUDED.verified_at
		WHERE iap_subscriptions.user_id = EXCLUDED.user_id
		RETURNING id::text
	`, subscription.UserID, subscription.ItemKey, subscription.ProductID, subscription.SubscriptionKey,
		orderDatabaseID, subscription.PurchaseTokenHash, subscription.PurchaseTokenCiphertext,
		subscription.Status, subscription.AutoRenewing, subscription.StartsAt, subscription.ExpiresAt,
		subscription.VerifiedAt).Scan(&subscriptionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ApplyResult{}, errors.New("subscription is already bound to another OneBeat account")
	}
	if err != nil {
		return ApplyResult{}, fmt.Errorf("upsert subscription: %w", err)
	}

	active := subscription.ExpiresAt.After(now) &&
		(subscription.Status == "ACTIVE" || subscription.Status == "CANCELED_ACTIVE")
	if active {
		if _, err := tx.Exec(ctx, `
			INSERT INTO entitlement_grants
				(user_id, entitlement_key, source_type, source_ref, starts_at, ends_at)
			VALUES ($1, 'pass.all', 'IAP_SUBSCRIPTION', $2, $3, $4)
			ON CONFLICT (source_type, source_ref, entitlement_key) DO UPDATE SET
				starts_at = EXCLUDED.starts_at,
				ends_at = EXCLUDED.ends_at,
				revoked_at = NULL,
				revoke_reason = NULL,
				updated_at = EXCLUDED.updated_at
		`, subscription.UserID, subscriptionID, subscription.StartsAt, subscription.ExpiresAt); err != nil {
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
	return ApplyResult{OrderDatabaseID: orderDatabaseID, NeedsConfirm: active && !order.Finished && !alreadyAcknowledged}, nil
}

func (r *Repository) MarkOrderAcknowledged(ctx context.Context, orderDatabaseID string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE iap_orders SET acknowledged_at = COALESCE(acknowledged_at, $2), updated_at = $2
		WHERE id = $1
	`, orderDatabaseID, now)
	return err
}

func upsertOrder(ctx context.Context, tx pgx.Tx, record OrderRecord, now time.Time) (string, bool, error) {
	receipt, err := json.Marshal(record.Receipt)
	if err != nil {
		return "", false, fmt.Errorf("encode receipt snapshot: %w", err)
	}
	var orderDatabaseID string
	var acknowledgedAt *time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO iap_orders
			(user_id, item_key, huawei_product_id, product_type, huawei_order_id,
			 original_order_id, purchase_token_hash, purchase_token_ciphertext,
			 developer_payload, status, purchased_at, expires_at, acknowledged_at,
			 verified_at, receipt_snapshot)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
			(CASE WHEN $13::boolean THEN $14::timestamptz END), $14::timestamptz, $15::jsonb)
		ON CONFLICT (huawei_order_id) DO UPDATE SET
			status = EXCLUDED.status,
			expires_at = EXCLUDED.expires_at,
			verified_at = EXCLUDED.verified_at,
			receipt_snapshot = EXCLUDED.receipt_snapshot,
			updated_at = EXCLUDED.verified_at,
			acknowledged_at = COALESCE(iap_orders.acknowledged_at, EXCLUDED.acknowledged_at)
		WHERE iap_orders.user_id = EXCLUDED.user_id
		  AND iap_orders.huawei_product_id = EXCLUDED.huawei_product_id
		RETURNING id::text, acknowledged_at
	`, record.UserID, record.ItemKey, record.ProductID, record.ProductType, record.OrderID,
		record.OriginalOrderID, record.PurchaseTokenHash, record.PurchaseTokenCiphertext,
		record.DeveloperPayload, record.Status, record.PurchasedAt, record.ExpiresAt,
		record.Finished, now, receipt).Scan(&orderDatabaseID, &acknowledgedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, errors.New("order is already bound to another OneBeat account or product")
	}
	if err != nil {
		return "", false, fmt.Errorf("upsert IAP order: %w", err)
	}
	return orderDatabaseID, acknowledgedAt != nil, nil
}
