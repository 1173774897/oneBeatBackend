package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	UserStatusActive  = "ACTIVE"
	UserStatusDeleted = "DELETED"
)

var (
	ErrUserNotFound  = errors.New("user not found")
	ErrUserNotActive = errors.New("user account is not active")
)

// UserStatus returns the account status for an authenticated session subject.
// Sessions are stateless JWTs, so this primary-key read is what turns a token
// issued before deletion into an invalid login.
func (r *Repository) UserStatus(ctx context.Context, userID string) (string, error) {
	var status string
	err := r.pool.QueryRow(ctx, `SELECT status FROM users WHERE id = $1`, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUserNotFound
	}
	if err != nil {
		return "", fmt.Errorf("query user status: %w", err)
	}
	return status, nil
}

// DeleteAccount de-identifies one account in a single transaction and returns
// when it was deleted. Repeating it for a deleted account returns the original
// time, so a client that lost the first response can retry safely.
//
// Only free entitlements live here (paid purchases stay with the Huawei account
// and are resolved on the device), so deletion forfeits redemption gifts only.
// What remains is history that can no longer be linked to a Huawei account:
// grant/audit rows and redemptions. Redemption usage is deliberately left in
// place under the deleted account; a new account for the same Huawei identity
// starts with fresh per-account limits. This loophole was accepted on
// 2026-11-03 in exchange for keeping no identifier.
func (r *Repository) DeleteAccount(ctx context.Context, userID string, now time.Time) (time.Time, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback(ctx)

	// The same row lock serializes deletion with redemptions.
	var status string
	var deletedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT status, deleted_at FROM users WHERE id = $1 FOR UPDATE`, userID).
		Scan(&status, &deletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrUserNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("lock deleted user: %w", err)
	}
	if status == UserStatusDeleted && deletedAt != nil {
		return deletedAt.UTC(), nil
	}

	// A random hash detaches the Huawei account (UpsertUser then creates a new
	// account for it) while keeping the NOT NULL UNIQUE column valid.
	if _, err := tx.Exec(ctx, `
		UPDATE users
		SET status = 'DELETED', deleted_at = $2, updated_at = $2,
		    huawei_union_id_hash = gen_random_bytes(32)
		WHERE id = $1
	`, userID, now); err != nil {
		return time.Time{}, fmt.Errorf("de-identify user: %w", err)
	}

	// Revoke every grant still open and record why, so an audit sees an
	// explicit end instead of a silently vanished entitlement.
	if _, err := tx.Exec(ctx, `
		WITH revoked AS (
			UPDATE entitlement_grants
			SET revoked_at = $2, revoke_reason = 'ACCOUNT_DELETED', updated_at = $2
			WHERE user_id = $1 AND revoked_at IS NULL AND (ends_at IS NULL OR ends_at > $2)
			RETURNING entitlement_key, source_type, source_ref
		)
		INSERT INTO entitlement_audit_logs
			(user_id, entitlement_key, event_type, source_type, source_ref, occurred_at)
		SELECT $1, entitlement_key, 'REVOKED', 'ACCOUNT_DELETION', source_ref, $2
		FROM revoked
	`, userID, now); err != nil {
		return time.Time{}, fmt.Errorf("revoke deleted user grants: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return time.Time{}, fmt.Errorf("commit account deletion: %w", err)
	}
	return now.UTC(), nil
}
