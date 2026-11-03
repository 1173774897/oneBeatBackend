package service

import (
	"context"
	"errors"
	"regexp"
	"time"

	"onebeat/store-api/internal/store/catalog"
	"onebeat/store-api/internal/store/redemption"
	"onebeat/store-api/internal/store/repository"
)

// Since 2026-11-03 paid Huawei purchases are verified, confirmed and resolved
// on the device and never reach this (Hong Kong) server, which keeps purchase
// and order records out of the cross-border transfer. This service handles the
// Huawei account session, free redemptions and account deletion only.

var (
	ErrRedemptionRequest             = errors.New("invalid redemption request")
	ErrRedemptionInvalid             = errors.New("redemption code is invalid or unavailable")
	ErrRedemptionNotStarted          = errors.New("redemption campaign has not started")
	ErrRedemptionAccountLimit        = errors.New("redemption account limit reached")
	ErrRedemptionGlobalLimit         = errors.New("redemption global limit reached")
	ErrRedemptionIdempotencyConflict = errors.New("redemption idempotency conflict")
)

type Service struct {
	repository              *repository.Repository
	now                     func() time.Time
	redemptionCodes         *redemption.CodeBook
	redemptionMonthDuration time.Duration
}

// RedeemInput is the authenticated request body. IdempotencyKey is scoped to
// the current OneBeat user rather than globally.
type RedeemInput struct {
	IdempotencyKey string `json:"idempotencyKey"`
	Code           string `json:"code"`
}

// RedeemResult describes the month added by this request and the aggregated
// pass snapshot after the transaction commits.
type RedeemResult struct {
	CampaignKey   string               `json:"campaignKey"`
	RedeemedAt    time.Time            `json:"redeemedAt"`
	GrantStartsAt time.Time            `json:"grantStartsAt"`
	GrantEndsAt   time.Time            `json:"grantEndsAt"`
	Pass          catalog.PassSnapshot `json:"pass"`
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func New(repository *repository.Repository, environment string) *Service {
	return &Service{
		repository:              repository,
		redemptionMonthDuration: redemption.MonthDurationForEnvironment(environment),
		now:                     time.Now,
	}
}

// ConfigureRedemptions installs the startup-validated private code book.
func (s *Service) ConfigureRedemptions(codeBook *redemption.CodeBook) {
	s.redemptionCodes = codeBook
}

// Redeem validates a secret code and applies one environment-specific month.
func (s *Service) Redeem(ctx context.Context, userID string, input RedeemInput, requestID string) (RedeemResult, error) {
	if s.redemptionCodes == nil || !uuidPattern.MatchString(input.IdempotencyKey) || input.Code == "" {
		return RedeemResult{}, ErrRedemptionRequest
	}
	// Resolve idempotency before rejecting the code so a reused key with changed
	// input is reported as a conflict instead of leaking a misleading code error.
	existing, found, err := s.repository.FindRedemptionByIdempotency(ctx, userID, input.IdempotencyKey)
	if err != nil {
		return RedeemResult{}, err
	}
	codeKey, matched := s.redemptionCodes.Match(input.Code)
	if !matched {
		if found {
			return RedeemResult{}, ErrRedemptionIdempotencyConflict
		}
		return RedeemResult{}, ErrRedemptionInvalid
	}
	now := s.now().UTC()
	campaign, exists := catalog.FindCampaignByCodeKey(codeKey)
	if !exists {
		return RedeemResult{}, ErrRedemptionInvalid
	}
	if found {
		if existing.CodeKey != codeKey {
			return RedeemResult{}, ErrRedemptionIdempotencyConflict
		}
		return s.redemptionResult(ctx, userID, existing)
	}
	if now.Before(campaign.StartsAt) {
		return RedeemResult{}, ErrRedemptionNotStarted
	}
	if !now.Before(campaign.EndsAt) {
		return RedeemResult{}, ErrRedemptionInvalid
	}
	if campaign.GlobalLimit > 0 {
		// The campaign cap is intentionally approximate. This unlocked count avoids
		// a single activity-wide lock and may permit a small burst over the limit.
		approximateCount, err := s.repository.ApproximateCampaignRedemptionCount(ctx, campaign.CampaignKey)
		if err != nil {
			return RedeemResult{}, err
		}
		if approximateCount >= campaign.GlobalLimit {
			return RedeemResult{}, ErrRedemptionGlobalLimit
		}
	}
	record, err := s.repository.ApplyRedemption(ctx, repository.ApplyRedemptionInput{
		UserID: userID, CampaignKey: campaign.CampaignKey, CodeKey: codeKey,
		ConfigVersion: catalog.ConfigVersion, IdempotencyKey: input.IdempotencyKey,
		PerAccountLimit:    campaign.PerAccountLimit,
		GrantMonthDuration: s.redemptionMonthDuration,
		Now:                now, RequestID: requestID,
	})
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrRedemptionAccountLimit):
			return RedeemResult{}, ErrRedemptionAccountLimit
		case errors.Is(err, repository.ErrRedemptionIdempotencyConflict):
			return RedeemResult{}, ErrRedemptionIdempotencyConflict
		default:
			return RedeemResult{}, err
		}
	}
	return s.redemptionResult(ctx, userID, record)
}

func (s *Service) redemptionResult(ctx context.Context, userID string, record repository.RedemptionRecord) (RedeemResult, error) {
	bootstrap, err := s.Bootstrap(ctx, userID)
	if err != nil {
		return RedeemResult{}, err
	}
	return RedeemResult{
		CampaignKey: record.CampaignKey, RedeemedAt: record.RedeemedAt,
		GrantStartsAt: record.GrantStartsAt, GrantEndsAt: record.GrantEndsAt,
		Pass: bootstrap.Pass,
	}, nil
}

// DeleteAccount de-identifies the account (repository.DeleteAccount). Paid
// purchases are unaffected: they belong to the Huawei account and Huawei keeps
// billing any subscription independently of this server.
func (s *Service) DeleteAccount(ctx context.Context, userID string) (time.Time, error) {
	return s.repository.DeleteAccount(ctx, userID, s.now().UTC())
}

// Bootstrap returns the catalog with the account's free grants (redemption
// pass chain, admin grants). Paid access is merged on the device.
func (s *Service) Bootstrap(ctx context.Context, userID string) (catalog.Bootstrap, error) {
	return s.repository.Bootstrap(ctx, userID, s.now().UTC())
}
