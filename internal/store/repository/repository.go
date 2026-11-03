package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"onebeat/store-api/internal/store/catalog"
)

// Repository holds account, redemption and free-grant data only. Paid Huawei
// purchases are verified and resolved on the device since 2026-11-03, so no
// purchase or order data is stored here (Store server is outside mainland China).
type Repository struct {
	pool *pgxpool.Pool
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
		// The schema only allows REDEMPTION and ADMIN grants (purchases stay on the device). ADMIN
		// keeps the historical "owned" label; clients treat it as permanent access.
		reason := catalog.AccessIAPPurchase
		if sourceType == "REDEMPTION" {
			reason = catalog.AccessRedemption
		}
		if itemKey != "pass.all" {
			grants[itemKey] = catalog.Access{Allowed: true, Reason: reason, ValidUntil: endsAt}
		}
	}
	if err := rows.Err(); err != nil {
		return catalog.Bootstrap{}, fmt.Errorf("iterate entitlement grants: %w", err)
	}

	// Paid purchases and the Huawei pass are resolved on the device
	// (2026-11-03: no purchase data reaches this server). The server only
	// aggregates the current redemption chain through its final month.
	{
		var redemptionExpiresAt *time.Time
		err = r.pool.QueryRow(ctx, `
			SELECT max(r.grant_ends_at)
			FROM redemptions r
			JOIN entitlement_grants g ON g.id = r.entitlement_grant_id
			WHERE r.user_id = $1
			  AND g.revoked_at IS NULL
			  AND r.chain_id IN (
				SELECT active.chain_id
				FROM redemptions active
				JOIN entitlement_grants active_grant ON active_grant.id = active.entitlement_grant_id
				WHERE active.user_id = $1
				  AND active_grant.revoked_at IS NULL
				  AND active.grant_starts_at <= $2
				  AND active.grant_ends_at > $2
			  )
		`, userID, now).Scan(&redemptionExpiresAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return catalog.Bootstrap{}, fmt.Errorf("query redemption pass: %w", err)
		}
		if err == nil && redemptionExpiresAt != nil && redemptionExpiresAt.After(now) {
			pass.Active = true
			pass.Source = catalog.AccessRedemption
			pass.Status = "ACTIVE"
			pass.ExpiresAt = redemptionExpiresAt
			pass.AutoRenewing = false
		}
	}

	return catalog.AuthenticatedBootstrap(now, grants, pass, catalog.ShareRewardSnapshot{}), nil
}
