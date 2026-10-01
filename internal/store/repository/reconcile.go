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
		SELECT item_key, huawei_product_id, product_type, huawei_order_id,
		       purchase_token_ciphertext, developer_payload
		FROM iap_orders
		WHERE user_id = $1
		ORDER BY verified_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list stored purchases: %w", err)
	}
	defer rows.Close()

	purchases := make([]StoredPurchaseRow, 0)
	seenOrders := make(map[string]struct{})

	subRows, err := r.pool.Query(ctx, `
		SELECT s.item_key, s.huawei_product_id, o.huawei_order_id,
		       s.purchase_token_ciphertext, o.developer_payload
		FROM iap_subscriptions s
		JOIN iap_orders o ON o.id = s.latest_order_id
		WHERE s.user_id = $1
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list stored subscriptions: %w", err)
	}
	for subRows.Next() {
		var row StoredPurchaseRow
		if err := subRows.Scan(
			&row.ItemKey, &row.ProductID, &row.OrderID,
			&row.PurchaseTokenCiphertext, &row.DeveloperPayload,
		); err != nil {
			subRows.Close()
			return nil, fmt.Errorf("scan stored subscription: %w", err)
		}
		row.ProductType = "AUTORENEWABLE"
		seenOrders[row.OrderID] = struct{}{}
		purchases = append(purchases, row)
	}
	subRows.Close()
	if err := subRows.Err(); err != nil {
		return nil, err
	}

	for rows.Next() {
		var row StoredPurchaseRow
		if err := rows.Scan(
			&row.ItemKey, &row.ProductID, &row.ProductType, &row.OrderID,
			&row.PurchaseTokenCiphertext, &row.DeveloperPayload,
		); err != nil {
			return nil, fmt.Errorf("scan stored purchase: %w", err)
		}
		if _, ok := seenOrders[row.OrderID]; ok {
			continue
		}
		seenOrders[row.OrderID] = struct{}{}
		purchases = append(purchases, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return purchases, nil
}
