package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type WebhookAcquireResult struct {
	EventDatabaseID  string
	AlreadyProcessed bool
}

type OrderLookup struct {
	UserID           string
	ItemKey          string
	ProductID        string
	ProductType      string
	DeveloperPayload string
	SubscriptionID   *string
}

func (r *Repository) AcquireWebhookEvent(
	ctx context.Context,
	huaweiEventID string,
	environment string,
	notificationType string,
	notificationSubtype string,
	signatureValid bool,
	payload json.RawMessage,
) (WebhookAcquireResult, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return WebhookAcquireResult{}, err
	}
	defer tx.Rollback(ctx)

	var eventID string
	var status string
	err = tx.QueryRow(ctx, `
		SELECT id::text, status
		FROM iap_webhook_events
		WHERE provider = 'HUAWEI' AND environment = $1 AND huawei_event_id = $2
		FOR UPDATE
	`, environment, huaweiEventID).Scan(&eventID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			INSERT INTO iap_webhook_events
				(provider, environment, huawei_event_id, notification_type,
				 notification_subtype, signature_valid, payload, status)
			VALUES ('HUAWEI', $1, $2, $3, $4, $5, $6, 'PROCESSING')
			RETURNING id::text
		`, environment, huaweiEventID, notificationType, notificationSubtype,
			signatureValid, payload).Scan(&eventID)
		if err != nil {
			return WebhookAcquireResult{}, fmt.Errorf("insert webhook event: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return WebhookAcquireResult{}, err
		}
		return WebhookAcquireResult{EventDatabaseID: eventID}, nil
	}
	if err != nil {
		return WebhookAcquireResult{}, fmt.Errorf("lock webhook event: %w", err)
	}
	if status == "PROCESSED" {
		if err := tx.Commit(ctx); err != nil {
			return WebhookAcquireResult{}, err
		}
		return WebhookAcquireResult{EventDatabaseID: eventID, AlreadyProcessed: true}, nil
	}
	_, err = tx.Exec(ctx, `
		UPDATE iap_webhook_events
		SET status = 'PROCESSING', attempt_count = attempt_count + 1, last_error = NULL
		WHERE id = $1
	`, eventID)
	if err != nil {
		return WebhookAcquireResult{}, fmt.Errorf("mark webhook processing: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return WebhookAcquireResult{}, err
	}
	return WebhookAcquireResult{EventDatabaseID: eventID}, nil
}

func (r *Repository) FinishWebhookEvent(
	ctx context.Context,
	eventDatabaseID string,
	status string,
	lastError string,
	processedAt time.Time,
) error {
	if status != "PROCESSED" && status != "FAILED" {
		return fmt.Errorf("invalid webhook status %q", status)
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE iap_webhook_events
		SET status = $2,
		    last_error = NULLIF($3, ''),
		    processed_at = CASE WHEN $2 = 'PROCESSED' THEN $4 ELSE processed_at END
		WHERE id = $1
	`, eventDatabaseID, status, lastError, processedAt)
	return err
}

func (r *Repository) MarkWebhookSignatureValid(ctx context.Context, eventDatabaseID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE iap_webhook_events SET signature_valid = true
		WHERE id = $1
	`, eventDatabaseID)
	return err
}

func (r *Repository) FindPurchaseOwner(
	ctx context.Context,
	environment string,
	purchaseOrderID string,
	purchaseTokenHash []byte,
	developerPayload string,
) (OrderLookup, error) {
	var lookup OrderLookup
	scanTransaction := func(row pgx.Row) error {
		return row.Scan(
			&lookup.UserID, &lookup.ItemKey, &lookup.ProductID, &lookup.ProductType,
			&lookup.DeveloperPayload, &lookup.SubscriptionID,
		)
	}

	// Keep the provider-identity priority explicit while allowing each lookup to
	// use its selective index. A single OR query can degrade into scanning every
	// transaction in an environment once PostgreSQL switches to a generic plan.
	err := pgx.ErrNoRows
	if purchaseOrderID != "" {
		err = scanTransaction(r.pool.QueryRow(ctx, `
			SELECT user_id::text, item_key, huawei_product_id, product_type,
			       developer_payload, subscription_id::text
			FROM iap_provider_transactions
			WHERE provider = 'HUAWEI' AND environment = $1 AND purchase_order_id = $2
			ORDER BY verified_at DESC
			LIMIT 1
		`, environment, purchaseOrderID))
	}
	if errors.Is(err, pgx.ErrNoRows) && len(purchaseTokenHash) > 0 {
		err = scanTransaction(r.pool.QueryRow(ctx, `
			SELECT user_id::text, item_key, huawei_product_id, product_type,
			       developer_payload, subscription_id::text
			FROM iap_provider_transactions
			WHERE provider = 'HUAWEI' AND environment = $1 AND purchase_token_hash = $2
			ORDER BY verified_at DESC
			LIMIT 1
		`, environment, purchaseTokenHash))
	}
	if errors.Is(err, pgx.ErrNoRows) && developerPayload != "" {
		err = scanTransaction(r.pool.QueryRow(ctx, `
			SELECT user_id::text, item_key, huawei_product_id, product_type,
			       developer_payload, subscription_id::text
			FROM iap_provider_transactions
			WHERE provider = 'HUAWEI' AND environment = $1 AND developer_payload = $2
			ORDER BY verified_at DESC
			LIMIT 1
		`, environment, developerPayload))
	}
	if errors.Is(err, pgx.ErrNoRows) && developerPayload != "" {
		err = r.pool.QueryRow(ctx, `
			SELECT user_id::text, item_key, huawei_product_id, 'AUTORENEWABLE',
			       developer_payload, id::text
			FROM iap_subscriptions
			WHERE provider = 'HUAWEI' AND environment = $1 AND developer_payload = $2
			ORDER BY verified_at DESC
			LIMIT 1
		`, environment, developerPayload).Scan(
			&lookup.UserID, &lookup.ItemKey, &lookup.ProductID, &lookup.ProductType,
			&lookup.DeveloperPayload, &lookup.SubscriptionID,
		)
	}
	if errors.Is(err, pgx.ErrNoRows) && developerPayload != "" {
		err = r.pool.QueryRow(ctx, `
			SELECT id::text FROM users
			WHERE developer_payload = $1 AND status = 'ACTIVE'
		`, developerPayload).Scan(&lookup.UserID)
		if err == nil {
			lookup.DeveloperPayload = developerPayload
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderLookup{}, errors.New("purchase owner not found")
	}
	if err != nil {
		return OrderLookup{}, fmt.Errorf("lookup purchase owner: %w", err)
	}
	return lookup, nil
}

func (r *Repository) InsertEntitlementAudit(
	ctx context.Context,
	userID string,
	entitlementKey string,
	eventType string,
	sourceType string,
	sourceRef *string,
	requestID string,
) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO entitlement_audit_logs
			(user_id, entitlement_key, event_type, source_type, source_ref, request_id)
		VALUES ($1, $2, $3, $4, $5::uuid, NULLIF($6, ''))
	`, userID, entitlementKey, eventType, sourceType, sourceRef, requestID)
	return err
}
