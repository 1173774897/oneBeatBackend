package repository

import (
	"context"
	"time"
)

func (r *Repository) MarkHuaweiOrderStatus(ctx context.Context, userID string, huaweiOrderID string, status string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE iap_orders
		SET status = $3, updated_at = $4, verified_at = $4
		WHERE user_id = $1 AND huawei_order_id = $2 AND status IS DISTINCT FROM $3
	`, userID, huaweiOrderID, status, now)
	return err
}
