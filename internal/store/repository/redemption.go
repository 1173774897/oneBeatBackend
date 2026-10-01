package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"onebeat/store-api/internal/store/redemption"
)

var (
	ErrRedemptionAccountLimit        = errors.New("redemption account limit reached")
	ErrRedemptionIAPActive           = errors.New("active IAP subscription prevents redemption")
	ErrRedemptionIdempotencyConflict = errors.New("redemption idempotency key belongs to another code")
	ErrRedemptionChainInvalid        = errors.New("redemption chain requires repair")
)

// RedemptionRecord is the persisted month segment returned for a new grant or
// an idempotent replay.
type RedemptionRecord struct {
	ID               string
	CampaignKey      string
	CodeKey          string
	RedeemedAt       time.Time
	GrantStartsAt    time.Time
	GrantEndsAt      time.Time
	PassExpiresAt    time.Time
	IdempotentReplay bool
}

// ApplyRedemptionInput contains only values already validated by the service;
// ApplyRedemption still rechecks mutable account state inside its transaction.
type ApplyRedemptionInput struct {
	UserID          string
	CampaignKey     string
	CodeKey         string
	ConfigVersion   string
	IdempotencyKey  string
	PerAccountLimit int
	// GrantMonthDuration is zero for production calendar months and positive for
	// an accelerated non-production month.
	GrantMonthDuration time.Duration
	Now                time.Time
	RequestID          string
}

// ApproximateCampaignRedemptionCount intentionally takes an unlocked MVCC snapshot.
// Concurrent successful redemptions may overshoot a configured soft cap slightly.
func (r *Repository) ApproximateCampaignRedemptionCount(ctx context.Context, campaignKey string) (int64, error) {
	var count int64
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM redemptions WHERE campaign_key = $1
	`, campaignKey).Scan(&count); err != nil {
		return 0, fmt.Errorf("count campaign redemptions: %w", err)
	}
	return count, nil
}

// FindRedemptionByIdempotency returns a prior successful redemption for safe
// replay before any provider calls or quota checks are performed.
func (r *Repository) FindRedemptionByIdempotency(
	ctx context.Context,
	userID string,
	idempotencyKey string,
) (RedemptionRecord, bool, error) {
	return findRedemptionByIdempotency(ctx, r.pool, userID, idempotencyKey)
}

func findRedemptionByIdempotency(
	ctx context.Context,
	queryer interface {
		QueryRow(context.Context, string, ...interface{}) pgx.Row
	},
	userID string,
	idempotencyKey string,
) (RedemptionRecord, bool, error) {
	var record RedemptionRecord
	err := queryer.QueryRow(ctx, `
		SELECT id::text, campaign_key, code_key, redeemed_at, grant_starts_at, grant_ends_at,
		       (SELECT max(chain_record.grant_ends_at) FROM redemptions chain_record
		        WHERE chain_record.chain_id = redemptions.chain_id)
		FROM redemptions
		WHERE user_id = $1 AND idempotency_key = $2
	`, userID, idempotencyKey).Scan(
		&record.ID, &record.CampaignKey, &record.CodeKey, &record.RedeemedAt,
		&record.GrantStartsAt, &record.GrantEndsAt, &record.PassExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RedemptionRecord{}, false, nil
	}
	if err != nil {
		return RedemptionRecord{}, false, fmt.Errorf("find redemption by idempotency key: %w", err)
	}
	return record, true, nil
}

// HasActiveIAPSubscription checks the locally verified subscription snapshot.
// Redeem refreshes known Huawei tokens before relying on this value.
func (r *Repository) HasActiveIAPSubscription(ctx context.Context, userID string, now time.Time) (bool, error) {
	var active bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM iap_subscriptions
			WHERE user_id = $1 AND expires_at > $2
			  AND status IN ('ACTIVE', 'CANCELED_ACTIVE')
		)
	`, userID, now).Scan(&active)
	if err != nil {
		return false, fmt.Errorf("query active IAP subscription: %w", err)
	}
	return active, nil
}

// ApplyRedemption atomically appends one production or accelerated test month
// to a user's gift-pass chain. It retries only database failures for which
// replaying the whole transaction is safe.
func (r *Repository) ApplyRedemption(ctx context.Context, input ApplyRedemptionInput) (RedemptionRecord, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		result, err := r.applyRedemptionOnce(ctx, input)
		if !isSerializationFailure(err) {
			return result, err
		}
		lastErr = err
	}
	return RedemptionRecord{}, fmt.Errorf("redemption serialization retries exhausted: %w", lastErr)
}

func (r *Repository) applyRedemptionOnce(ctx context.Context, input ApplyRedemptionInput) (RedemptionRecord, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return RedemptionRecord{}, err
	}
	defer tx.Rollback(ctx)

	// The user row is the only serialization point. Different users never queue
	// behind a campaign-wide counter, while same-user idempotency and chain appends
	// remain ordered.
	var lockedUserID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM users WHERE id = $1 FOR UPDATE`, input.UserID).Scan(&lockedUserID); err != nil {
		return RedemptionRecord{}, fmt.Errorf("lock redemption user: %w", err)
	}
	if existing, found, err := findRedemptionByIdempotency(ctx, tx, input.UserID, input.IdempotencyKey); err != nil {
		return RedemptionRecord{}, err
	} else if found {
		if existing.CodeKey != input.CodeKey {
			return RedemptionRecord{}, ErrRedemptionIdempotencyConflict
		}
		existing.IdempotentReplay = true
		return existing, tx.Commit(ctx)
	}

	var iapActive bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM iap_subscriptions
			WHERE user_id = $1 AND expires_at > $2
			  AND status IN ('ACTIVE', 'CANCELED_ACTIVE')
		)
	`, input.UserID, input.Now).Scan(&iapActive); err != nil {
		return RedemptionRecord{}, fmt.Errorf("recheck active IAP subscription: %w", err)
	}
	if iapActive {
		return RedemptionRecord{}, ErrRedemptionIAPActive
	}

	// Per-account counting reuses the user lock above; this row does not create a
	// cross-user hotspot and is therefore kept exact even though the global cap is soft.
	if _, err := tx.Exec(ctx, `
		INSERT INTO redemption_account_usage (campaign_key, user_id) VALUES ($1, $2)
		ON CONFLICT (campaign_key, user_id) DO NOTHING
	`, input.CampaignKey, input.UserID); err != nil {
		return RedemptionRecord{}, fmt.Errorf("create redemption account counter: %w", err)
	}
	var accountCount int
	if err := tx.QueryRow(ctx, `
		SELECT redeemed_count FROM redemption_account_usage
		WHERE campaign_key = $1 AND user_id = $2 FOR UPDATE
	`, input.CampaignKey, input.UserID).Scan(&accountCount); err != nil {
		return RedemptionRecord{}, fmt.Errorf("lock redemption account counter: %w", err)
	}
	if input.PerAccountLimit > 0 && accountCount >= input.PerAccountLimit {
		return RedemptionRecord{}, ErrRedemptionAccountLimit
	}

	type chainState struct {
		id        string
		anchor    time.Time
		ordinal   int
		latestEnd time.Time
	}
	rows, err := tx.Query(ctx, `
		SELECT r.chain_id::text, r.chain_anchor_at, max(r.month_ordinal), max(r.grant_ends_at)
		FROM redemptions r
		JOIN entitlement_grants g ON g.id = r.entitlement_grant_id
		WHERE r.user_id = $1 AND g.revoked_at IS NULL AND r.grant_ends_at > $2
		GROUP BY r.chain_id, r.chain_anchor_at
		ORDER BY max(r.grant_ends_at) DESC
		LIMIT 2
	`, input.UserID, input.Now)
	if err != nil {
		return RedemptionRecord{}, fmt.Errorf("query active redemption chain: %w", err)
	}
	chains := make([]chainState, 0, 2)
	for rows.Next() {
		var chain chainState
		if err := rows.Scan(&chain.id, &chain.anchor, &chain.ordinal, &chain.latestEnd); err != nil {
			rows.Close()
			return RedemptionRecord{}, fmt.Errorf("scan redemption chain: %w", err)
		}
		chains = append(chains, chain)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RedemptionRecord{}, err
	}
	rows.Close()
	if len(chains) > 1 {
		return RedemptionRecord{}, ErrRedemptionChainInvalid
	}

	var redemptionID string
	var grantID string
	var generatedChainID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text, gen_random_uuid()::text`).Scan(
		&redemptionID, &grantID, &generatedChainID,
	); err != nil {
		return RedemptionRecord{}, fmt.Errorf("generate redemption identifiers: %w", err)
	}
	chainID := generatedChainID
	anchor := input.Now.UTC()
	monthOrdinal := 1
	grantMonthDuration := input.GrantMonthDuration
	eventType := "GRANTED"
	if len(chains) == 1 {
		chainID = chains[0].id
		anchor = chains[0].anchor.UTC()
		monthOrdinal = chains[0].ordinal + 1
		eventType = "EXTENDED"
		var validChain bool
		grantMonthDuration, validChain = redemptionChainMonthDuration(
			anchor,
			chains[0].ordinal,
			chains[0].latestEnd,
			grantMonthDuration,
		)
		if !validChain {
			// Never append to a broken chain: silently repairing it here could grant
			// overlapping or missing time and make later aggregation ambiguous.
			return RedemptionRecord{}, ErrRedemptionChainInvalid
		}
	}
	grantStartsAt := redemption.GrantBoundary(anchor, monthOrdinal-1, grantMonthDuration)
	grantEndsAt := redemption.GrantBoundary(anchor, monthOrdinal, grantMonthDuration)

	if _, err := tx.Exec(ctx, `
		INSERT INTO entitlement_grants
			(id, user_id, entitlement_key, source_type, source_ref, starts_at, ends_at)
		VALUES ($1, $2, 'pass.all', 'REDEMPTION', $3, $4, $5)
	`, grantID, input.UserID, redemptionID, grantStartsAt, grantEndsAt); err != nil {
		return RedemptionRecord{}, fmt.Errorf("insert redemption entitlement: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO redemptions
			(id, user_id, campaign_key, code_key, config_version, idempotency_key,
			 account_sequence, chain_id, chain_anchor_at, month_ordinal, redeemed_at,
			 grant_starts_at, grant_ends_at, entitlement_grant_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, redemptionID, input.UserID, input.CampaignKey, input.CodeKey, input.ConfigVersion,
		input.IdempotencyKey, accountCount+1, chainID, anchor, monthOrdinal, input.Now,
		grantStartsAt, grantEndsAt, grantID); err != nil {
		return RedemptionRecord{}, fmt.Errorf("insert redemption: %w", err)
	}
	before, _ := json.Marshal(map[string]interface{}{"source": "PASS_REDEMPTION", "expiresAt": grantStartsAt})
	after, _ := json.Marshal(map[string]interface{}{"source": "PASS_REDEMPTION", "chainId": chainID, "expiresAt": grantEndsAt})
	if _, err := tx.Exec(ctx, `
		INSERT INTO entitlement_audit_logs
			(user_id, entitlement_key, event_type, source_type, source_ref,
			 before_snapshot, after_snapshot, request_id, occurred_at)
		VALUES ($1, 'pass.all', $2, 'REDEMPTION', $3, $4, $5, $6, $7)
	`, input.UserID, eventType, redemptionID, before, after, input.RequestID, input.Now); err != nil {
		return RedemptionRecord{}, fmt.Errorf("insert redemption audit: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE redemption_account_usage
		SET redeemed_count = redeemed_count + 1, last_redeemed_at = $3
		WHERE campaign_key = $1 AND user_id = $2
	`, input.CampaignKey, input.UserID, input.Now); err != nil {
		return RedemptionRecord{}, fmt.Errorf("increment redemption account counter: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RedemptionRecord{}, err
	}
	return RedemptionRecord{
		ID: redemptionID, CampaignKey: input.CampaignKey, CodeKey: input.CodeKey,
		RedeemedAt: input.Now, GrantStartsAt: grantStartsAt, GrantEndsAt: grantEndsAt,
		PassExpiresAt: grantEndsAt,
	}, nil
}

// redemptionChainMonthDuration preserves the cadence already persisted for an
// active chain. This keeps legacy test grants immutable when acceleration is
// introduced while requiring all newly created chains to use the configured mode.
func redemptionChainMonthDuration(
	anchor time.Time,
	monthOrdinal int,
	latestEnd time.Time,
	configuredDuration time.Duration,
) (time.Duration, bool) {
	if latestEnd.Equal(redemption.GrantBoundary(anchor, monthOrdinal, configuredDuration)) {
		return configuredDuration, true
	}
	if configuredDuration > 0 && latestEnd.Equal(redemption.CalendarBoundary(anchor, monthOrdinal)) {
		return 0, true
	}
	return 0, false
}

func isSerializationFailure(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && (pgError.Code == "40001" || pgError.Code == "40P01")
}
