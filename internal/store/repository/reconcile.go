package repository

import (
	"context"
	"fmt"
)

type StoredPurchaseRow struct {
	ItemKey                 string
	ProductID               string
	ProductType             string
	OrderID                 string
	PurchaseTokenCiphertext []byte
	DeveloperPayload        string
}

func (r *Repository) ListStoredPurchasesForUser(ctx context.Context, userID string) ([]StoredPurchaseRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT s.item_key, s.huawei_product_id, 'AUTORENEWABLE',
		       s.latest_purchase_order_id, t.purchase_token_ciphertext, s.developer_payload
		FROM iap_subscriptions s
		JOIN LATERAL (
			SELECT purchase_token_ciphertext
			FROM iap_subscription_tokens
			WHERE subscription_id = s.id
			ORDER BY last_seen_at DESC
			LIMIT 1
		) t ON true
		WHERE s.user_id = $1 AND s.latest_purchase_order_id IS NOT NULL
		UNION ALL
		SELECT p.item_key, p.huawei_product_id, p.product_type,
		       p.purchase_order_id, p.purchase_token_ciphertext, p.developer_payload
		FROM iap_provider_transactions p
		WHERE p.user_id = $1 AND p.trade_type = 'PURCHASE'
		  AND p.product_type = 'NONCONSUMABLE'
		ORDER BY 2, 4
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list stored purchases: %w", err)
	}
	defer rows.Close()

	purchases := make([]StoredPurchaseRow, 0)
	for rows.Next() {
		var row StoredPurchaseRow
		if err := rows.Scan(
			&row.ItemKey, &row.ProductID, &row.ProductType, &row.OrderID,
			&row.PurchaseTokenCiphertext, &row.DeveloperPayload,
		); err != nil {
			return nil, fmt.Errorf("scan stored purchase: %w", err)
		}
		purchases = append(purchases, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return purchases, nil
}
