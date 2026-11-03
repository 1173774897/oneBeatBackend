package repository

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	value := make([]byte, n)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return value
}

// Online account deletion (2026-11-03): de-identifies the account, revokes the
// free grants this server holds, keeps redemptions, and lets the Huawei
// identity register again. Paid purchases are never stored here.
func TestDeleteAccountDeidentifiesAndKeepsRedemptions(t *testing.T) {
	databaseURL := os.Getenv("ONEBEAT_INTEGRATION_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ONEBEAT_INTEGRATION_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := New(pool)
	now := time.Now().UTC().Truncate(time.Second)

	unionHash := randomBytes(t, 32)
	userID, err := repo.UpsertUser(ctx, unionHash, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO entitlement_grants (user_id, entitlement_key, source_type, source_ref, starts_at, ends_at)
		VALUES ($1, 'pass.all', 'REDEMPTION', gen_random_uuid(), $2, $3)
	`, userID, now.Add(-time.Hour), now.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO redemption_account_usage (campaign_key, user_id, redeemed_count, last_redeemed_at)
		VALUES ('integration-campaign', $1, 1, $2)
	`, userID, now); err != nil {
		t.Fatal(err)
	}

	deletedAt, err := repo.DeleteAccount(ctx, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	if !deletedAt.Equal(now) {
		t.Fatalf("deletedAt = %s, want %s", deletedAt, now)
	}

	var status string
	var storedHash []byte
	if err := pool.QueryRow(ctx, `SELECT status, huawei_union_id_hash FROM users WHERE id = $1`, userID).
		Scan(&status, &storedHash); err != nil {
		t.Fatal(err)
	}
	if status != UserStatusDeleted || bytes.Equal(storedHash, unionHash) {
		t.Fatalf("user not de-identified: status=%s sameHash=%v", status, bytes.Equal(storedHash, unionHash))
	}
	var openGrants, revokedAudits, redemptionRows int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM entitlement_grants WHERE user_id = $1 AND revoked_at IS NULL),
			(SELECT count(*) FROM entitlement_audit_logs WHERE user_id = $1 AND source_type = 'ACCOUNT_DELETION'),
			(SELECT count(*) FROM redemption_account_usage WHERE user_id = $1)
	`, userID).Scan(&openGrants, &revokedAudits, &redemptionRows); err != nil {
		t.Fatal(err)
	}
	if openGrants != 0 || revokedAudits != 1 || redemptionRows != 1 {
		t.Fatalf("grants open=%d audits=%d redemptions kept=%d", openGrants, revokedAudits, redemptionRows)
	}
	bootstrap, err := repo.Bootstrap(ctx, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.Pass.Active {
		t.Fatal("a revoked gift pass must not stay active")
	}

	again, err := repo.DeleteAccount(ctx, userID, now.Add(time.Hour))
	if err != nil || !again.Equal(now) {
		t.Fatalf("repeat deletion = %s, %v", again, err)
	}
	if status, err := repo.UserStatus(ctx, userID); err != nil || status != UserStatusDeleted {
		t.Fatalf("UserStatus = %s, %v", status, err)
	}
	newUserID, err := repo.UpsertUser(ctx, unionHash, now.Add(2*time.Hour))
	if err != nil || newUserID == userID {
		t.Fatalf("same Huawei identity must register a new account: %s, %v", newUserID, err)
	}
	if _, err := repo.DeleteAccount(ctx, "00000000-0000-4000-8000-000000000000", now); err != ErrUserNotFound {
		t.Fatalf("unknown user = %v", err)
	}
}
