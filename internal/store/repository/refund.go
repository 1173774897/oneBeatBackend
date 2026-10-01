package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type RefundRecord struct {
	Environment             string
	UserID                  string
	SubscriptionID          *string
	ItemKey                 string
	ProductID               string
	ProductType             string
	OrderID                 string
	PurchaseTokenHash       []byte
	PurchaseTokenCiphertext []byte
	DeveloperPayload        string
	RefundType              string
	EntitlementEffect       string
	ProviderStatus          string
	OccurredAt              time.Time
	Receipt                 interface{}
}

func (r *Repository) ApplyRefund(ctx context.Context, record RefundRecord, now time.Time) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var relatedID *string
	err = tx.QueryRow(ctx, `
		SELECT id::text
		FROM iap_provider_transactions
		WHERE provider = 'HUAWEI' AND environment = $1 AND trade_type = 'PURCHASE'
		  AND purchase_token_hash = $2
		  AND ($3::uuid IS NULL OR subscription_id = $3::uuid)
		ORDER BY occurred_at DESC
		LIMIT 1
	`, record.Environment, record.PurchaseTokenHash, record.SubscriptionID).Scan(&relatedID)
	if err != nil && err != pgx.ErrNoRows {
		return fmt.Errorf("find refunded purchase: %w", err)
	}
	if relatedID == nil && record.EntitlementEffect != "KEEP" {
		record.EntitlementEffect = "PENDING"
	}

	payload, err := json.Marshal(record.Receipt)
	if err != nil {
		return fmt.Errorf("encode refund snapshot: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO iap_provider_transactions
			(provider, environment, user_id, subscription_id, item_key, huawei_product_id,
			 product_type, purchase_order_id, purchase_token_hash, purchase_token_ciphertext,
			 developer_payload, trade_type, transaction_subtype, refund_type,
			 entitlement_effect, related_purchase_transaction_id, provider_status,
			 occurred_at, verified_at, payload_snapshot)
		VALUES ('HUAWEI', $1, $2, $3::uuid, $4, $5, $6, $7, $8, $9, $10,
			'REFUND', 'REFUND', $11, $12, $13::uuid, $14, $15, $16, $17::jsonb)
		ON CONFLICT (provider, environment, purchase_order_id, trade_type) DO UPDATE SET
			refund_type = EXCLUDED.refund_type,
			entitlement_effect = CASE
				WHEN iap_provider_transactions.entitlement_effect = 'REVOKE' THEN 'REVOKE'
				WHEN iap_provider_transactions.entitlement_effect = 'KEEP'
					AND EXCLUDED.entitlement_effect = 'PENDING' THEN 'KEEP'
				ELSE EXCLUDED.entitlement_effect
			END,
			related_purchase_transaction_id = COALESCE(
				iap_provider_transactions.related_purchase_transaction_id,
				EXCLUDED.related_purchase_transaction_id
			),
			provider_status = EXCLUDED.provider_status,
			verified_at = EXCLUDED.verified_at,
			payload_snapshot = EXCLUDED.payload_snapshot,
			updated_at = EXCLUDED.verified_at
	`, record.Environment, record.UserID, record.SubscriptionID, record.ItemKey,
		record.ProductID, record.ProductType, record.OrderID, record.PurchaseTokenHash,
		record.PurchaseTokenCiphertext, record.DeveloperPayload, record.RefundType,
		record.EntitlementEffect, relatedID, record.ProviderStatus, record.OccurredAt,
		now, redactPurchaseTokens(payload))
	if err != nil {
		return fmt.Errorf("upsert refund transaction: %w", err)
	}

	if record.EntitlementEffect == "REVOKE" && record.SubscriptionID != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE iap_subscriptions
			SET status = 'REVOKED', auto_renewing = false,
			    revoked_at = COALESCE(revoked_at, $2), verified_at = $2,
			    version = version + 1, updated_at = $2
			WHERE id = $1
		`, *record.SubscriptionID, now); err != nil {
			return fmt.Errorf("revoke refunded subscription: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE iap_subscription_periods
			SET status = 'REVOKED', updated_at = $2
			WHERE subscription_id = $1 AND status <> 'REVOKED'
		`, *record.SubscriptionID, now); err != nil {
			return fmt.Errorf("revoke refunded periods: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE entitlement_grants
			SET revoked_at = $2, revoke_reason = 'REFUND', updated_at = $2
			WHERE source_type = 'IAP_SUBSCRIPTION' AND source_ref = $1 AND revoked_at IS NULL
		`, *record.SubscriptionID, now); err != nil {
			return fmt.Errorf("revoke refunded entitlement: %w", err)
		}
	}
	if record.EntitlementEffect == "REVOKE" && record.SubscriptionID == nil && relatedID != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE entitlement_grants
			SET revoked_at = $2, revoke_reason = 'REFUND', updated_at = $2
			WHERE source_type = 'IAP_NONCONSUMABLE' AND source_ref = $1 AND revoked_at IS NULL
		`, *relatedID, now); err != nil {
			return fmt.Errorf("revoke refunded non-consumable entitlement: %w", err)
		}
	}
	return tx.Commit(ctx)
}
