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
	EventDatabaseID string
	AlreadyProcessed bool
}

type OrderLookup struct {
	UserID           string
	ItemKey          string
	ProductID        string
	ProductType      string
	DeveloperPayload string
}

func (r *Repository) AcquireWebhookEvent(
	ctx context.Context,
	huaweiEventID string,
	environment string,
	eventType string,
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
		WHERE huawei_event_id = $1
		FOR UPDATE
	`, huaweiEventID).Scan(&eventID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			INSERT INTO iap_webhook_events
				(huawei_event_id, environment, event_type, signature_valid, payload, status)
			VALUES ($1, $2, $3, $4, $5, 'PROCESSING')
			RETURNING id::text
		`, huaweiEventID, environment, eventType, signatureValid, payload).Scan(&eventID)
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
		SET status = 'PROCESSING',
		    attempt_count = attempt_count + 1,
		    last_error = NULL
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

func (r *Repository) FindOrderByHuaweiOrderID(ctx context.Context, huaweiOrderID string) (OrderLookup, error) {
	var lookup OrderLookup
	err := r.pool.QueryRow(ctx, `
		SELECT user_id::text, item_key, huawei_product_id, product_type, developer_payload
		FROM iap_orders
		WHERE huawei_order_id = $1
	`, huaweiOrderID).Scan(
		&lookup.UserID, &lookup.ItemKey, &lookup.ProductID, &lookup.ProductType, &lookup.DeveloperPayload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderLookup{}, errors.New("order not found")
	}
	if err != nil {
		return OrderLookup{}, fmt.Errorf("lookup order: %w", err)
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
