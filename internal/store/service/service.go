package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	huawei_iap "onebeat/store-api/internal/huawei/iap"
	"onebeat/store-api/internal/store/catalog"
	"onebeat/store-api/internal/store/redemption"
	"onebeat/store-api/internal/store/repository"
	"onebeat/store-api/internal/store/security"
)

var (
	ErrInvalidPurchase               = errors.New("purchase verification failed")
	ErrHuaweiUnavailable             = errors.New("Huawei IAP is unavailable")
	ErrRedemptionRequest             = errors.New("invalid redemption request")
	ErrRedemptionInvalid             = errors.New("redemption code is invalid or unavailable")
	ErrRedemptionNotStarted          = errors.New("redemption campaign has not started")
	ErrRedemptionAccountLimit        = errors.New("redemption account limit reached")
	ErrRedemptionGlobalLimit         = errors.New("redemption global limit reached")
	ErrRedemptionIAPActive           = errors.New("active Huawei subscription prevents redemption")
	ErrRedemptionIdempotencyConflict = errors.New("redemption idempotency conflict")
)

type IAPClient interface {
	DecodePurchaseData(string) (huawei_iap.PurchaseReference, error)
	VerifyNotification(string) (huawei_iap.NotificationPayload, error)
	QueryOrder(context.Context, string, string) (huawei_iap.PurchaseOrderPayload, error)
	QuerySubscription(context.Context, string, string) (huawei_iap.SubGroupStatusPayload, error)
	ConfirmOrder(context.Context, string, string) error
	ConfirmSubscription(context.Context, string, string) error
	ValidateOrder(huawei_iap.PurchaseOrderPayload, string, int, string) error
	ValidateSubscription(huawei_iap.SubGroupStatusPayload, string, string) error
	NotificationEnvironmentMatches(string) bool
	NotificationApplicationMatches(string) bool
}

type Service struct {
	repository              *repository.Repository
	iap                     IAPClient
	purchaseBindingSecret   []byte
	tokenEncryptionKey      []byte
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

type VerifyInput struct {
	IdempotencyKey string `json:"idempotencyKey"`
	ProductID      string `json:"productId"`
	PurchaseData   string `json:"purchaseData"`
}

// ConfigureRedemptions installs the startup-validated private code book.
func (s *Service) ConfigureRedemptions(codeBook *redemption.CodeBook) {
	s.redemptionCodes = codeBook
}

// Redeem validates a secret code, refreshes known Huawei subscriptions outside
// the database transaction, and then applies one environment-specific month.
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
	// Huawei network calls must finish before ApplyRedemption acquires user and
	// account rows; otherwise a slow provider response would hold database locks.
	if err := s.refreshStoredSubscriptions(ctx, userID); err != nil {
		return RedeemResult{}, fmt.Errorf("%w: %v", ErrHuaweiUnavailable, err)
	}
	active, err := s.repository.HasActiveIAPSubscription(ctx, userID, now)
	if err != nil {
		return RedeemResult{}, err
	}
	if active {
		return RedeemResult{}, ErrRedemptionIAPActive
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
		case errors.Is(err, repository.ErrRedemptionIAPActive):
			return RedeemResult{}, ErrRedemptionIAPActive
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

func (s *Service) refreshStoredSubscriptions(ctx context.Context, userID string) error {
	rows, err := s.repository.ListStoredPurchasesForUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ProductType != catalog.ProductAutoRenewable {
			continue
		}
		token, err := security.DecryptToken(s.tokenEncryptionKey, row.PurchaseTokenCiphertext)
		if err != nil || token == "" {
			return errors.New("stored subscription token cannot be read")
		}
		item, ok := catalog.FindByProductID(row.ProductID)
		if !ok {
			item = catalog.Item{ItemKey: row.ItemKey, HuaweiProductID: row.ProductID, IAPProductType: row.ProductType}
		}
		developerPayload := row.DeveloperPayload
		if developerPayload == "" {
			developerPayload = s.DeveloperPayload(userID)
		}
		if err := s.reconcileSubscription(ctx, userID, item, row.OrderID, token, developerPayload); err != nil {
			return err
		}
	}
	return nil
}

func New(
	repository *repository.Repository,
	iap IAPClient,
	purchaseBindingSecret []byte,
	tokenEncryptionKey []byte,
	environment string,
) *Service {
	return &Service{
		repository: repository, iap: iap,
		purchaseBindingSecret:   append([]byte(nil), purchaseBindingSecret...),
		tokenEncryptionKey:      append([]byte(nil), tokenEncryptionKey...),
		redemptionMonthDuration: redemption.MonthDurationForEnvironment(environment),
		now:                     time.Now,
	}
}

func (s *Service) DeveloperPayload(userID string) string {
	return security.HMACSHA256Hex(s.purchaseBindingSecret, userID)
}

func (s *Service) Bootstrap(ctx context.Context, userID string) (catalog.Bootstrap, error) {
	now := s.now().UTC()
	developerPayload := s.DeveloperPayload(userID)
	if err := s.repository.BindDeveloperPayload(ctx, userID, developerPayload, now); err != nil {
		return catalog.Bootstrap{}, err
	}
	return s.repository.Bootstrap(ctx, userID, developerPayload, now)
}

func (s *Service) VerifyPurchase(ctx context.Context, userID string, input VerifyInput) (catalog.Bootstrap, error) {
	item, ok := catalog.FindByProductID(input.ProductID)
	if !ok || input.PurchaseData == "" || input.IdempotencyKey == "" {
		return catalog.Bootstrap{}, ErrInvalidPurchase
	}
	reference, err := s.iap.DecodePurchaseData(input.PurchaseData)
	if err != nil || reference.PurchaseOrderID == "" || reference.PurchaseToken == "" {
		return catalog.Bootstrap{}, fmt.Errorf("%w: invalid purchase data", ErrInvalidPurchase)
	}
	if reference.ProductID != "" && reference.ProductID != input.ProductID {
		return catalog.Bootstrap{}, fmt.Errorf("%w: product mismatch", ErrInvalidPurchase)
	}
	developerPayload := s.DeveloperPayload(userID)
	if item.IAPProductType == catalog.ProductAutoRenewable {
		err = s.verifySubscription(ctx, userID, item, reference, developerPayload)
	} else {
		err = s.verifyNonConsumable(ctx, userID, item, reference, developerPayload)
	}
	if err != nil {
		return catalog.Bootstrap{}, err
	}
	return s.Bootstrap(ctx, userID)
}

func (s *Service) RestorePurchases(ctx context.Context, userID string, purchases []VerifyInput) (catalog.Bootstrap, error) {
	if len(purchases) > 50 {
		return catalog.Bootstrap{}, fmt.Errorf("%w: restore batch size must be between 0 and 50", ErrInvalidPurchase)
	}
	successCount := 0
	var lastErr error
	for _, purchase := range purchases {
		if _, err := s.VerifyPurchase(ctx, userID, purchase); err != nil {
			lastErr = err
			continue
		}
		successCount++
	}
	reconcileErr := s.ReconcileStoredPurchases(ctx, userID)
	if reconcileErr != nil && successCount == 0 {
		stored, listErr := s.repository.ListStoredPurchasesForUser(ctx, userID)
		if listErr != nil || len(stored) == 0 {
			if lastErr != nil {
				return catalog.Bootstrap{}, lastErr
			}
			return catalog.Bootstrap{}, reconcileErr
		}
	}
	if successCount == 0 && len(purchases) > 0 && lastErr != nil && reconcileErr != nil {
		return catalog.Bootstrap{}, lastErr
	}
	return s.Bootstrap(ctx, userID)
}

func (s *Service) verifyNonConsumable(
	ctx context.Context,
	userID string,
	item catalog.Item,
	reference huawei_iap.PurchaseReference,
	developerPayload string,
) error {
	order, err := s.iap.QueryOrder(ctx, reference.PurchaseOrderID, reference.PurchaseToken)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrHuaweiUnavailable, err)
	}
	return s.verifyAndApplyNonConsumable(ctx, userID, item, reference, developerPayload, order)
}

func (s *Service) verifyAndApplyNonConsumable(
	ctx context.Context,
	userID string,
	item catalog.Item,
	reference huawei_iap.PurchaseReference,
	developerPayload string,
	order huawei_iap.PurchaseOrderPayload,
) error {
	expectedProductType := reference.ProductType
	if expectedProductType <= 0 {
		expectedProductType = 1
	}
	if err := s.iap.ValidateOrder(order, item.HuaweiProductID, expectedProductType, developerPayload); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPurchase, err)
	}
	if order.PurchaseOrderID != reference.PurchaseOrderID || order.PurchaseToken != reference.PurchaseToken {
		return fmt.Errorf("%w: authoritative order identity mismatch", ErrInvalidPurchase)
	}
	encryptedToken, err := security.EncryptToken(s.tokenEncryptionKey, order.PurchaseToken)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	status := "PURCHASED"
	if order.Revoked() {
		status = "REVOKED"
	}
	purchasedAt := order.PurchaseTime.Time()
	result, err := s.repository.ApplyNonConsumable(ctx, repository.OrderRecord{
		Environment: providerEnvironment(order.Environment),
		UserID:      userID, ItemKey: item.ItemKey, ProductID: item.HuaweiProductID,
		ProductType: catalog.ProductNonConsumable, OrderID: order.PurchaseOrderID,
		OriginalOrderID:   optionalString(order.OriginalPurchaseOrderID),
		PurchaseTokenHash: security.SHA256(order.PurchaseToken), PurchaseTokenCiphertext: encryptedToken,
		DeveloperPayload: developerPayload, Status: status, PurchasedAt: &purchasedAt,
		Finished: order.FinishStatus == "FINISHED", Receipt: order,
	}, now)
	if err != nil {
		return err
	}
	if result.NeedsConfirm {
		if err := s.iap.ConfirmOrder(ctx, order.PurchaseOrderID, order.PurchaseToken); err != nil {
			return fmt.Errorf("%w: %v", ErrHuaweiUnavailable, err)
		}
		if err := s.repository.MarkOrderAcknowledged(ctx, result.OrderDatabaseID, s.now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) verifySubscription(
	ctx context.Context,
	userID string,
	item catalog.Item,
	reference huawei_iap.PurchaseReference,
	developerPayload string,
) error {
	return s.verifySubscriptionFromSource(ctx, userID, item, reference, developerPayload, "CLIENT_RESTORE")
}

func (s *Service) verifySubscriptionFromSource(
	ctx context.Context,
	userID string,
	item catalog.Item,
	reference huawei_iap.PurchaseReference,
	developerPayload string,
	source string,
) error {
	subscription, err := s.iap.QuerySubscription(ctx, reference.PurchaseOrderID, reference.PurchaseToken)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrHuaweiUnavailable, err)
	}
	if err := s.iap.ValidateSubscription(subscription, item.HuaweiProductID, developerPayload); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPurchase, err)
	}
	state := subscription.LastSubscriptionStatus
	order := state.LastPurchaseOrder
	purchaseToken := state.PurchaseToken
	if purchaseToken == "" {
		purchaseToken = order.PurchaseToken
	}
	encryptedToken, err := security.EncryptToken(s.tokenEncryptionKey, purchaseToken)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	startsAt := order.PurchaseTime.Time()
	expiresAt := state.ExpiresTime.Time()
	if int64(order.PurchaseTime) <= 0 || int64(state.ExpiresTime) <= 0 || !expiresAt.After(startsAt) {
		return fmt.Errorf("%w: invalid subscription period", ErrInvalidPurchase)
	}
	status := subscriptionStatus(int(state.Status), int(state.RenewalInfo.AutoRenewStatusCode))
	finished := order.FinishStatus == "FINISHED"
	orderStatus := "PURCHASED"
	if order.Revoked() || status == "REVOKED" {
		orderStatus = "REVOKED"
	}
	orderResult, err := s.repository.ApplySubscription(ctx, repository.OrderRecord{
		Environment: providerEnvironment(subscription.Environment),
		UserID:      userID, ItemKey: item.ItemKey, ProductID: item.HuaweiProductID,
		ProductType: catalog.ProductAutoRenewable, OrderID: order.PurchaseOrderID,
		OriginalOrderID:   optionalString(order.OriginalPurchaseOrderID),
		PurchaseTokenHash: security.SHA256(purchaseToken), PurchaseTokenCiphertext: encryptedToken,
		DeveloperPayload: developerPayload, Status: orderStatus, PurchasedAt: &startsAt,
		ExpiresAt: &expiresAt, Finished: finished, Receipt: subscription,
	}, repository.SubscriptionRecord{
		Environment: providerEnvironment(subscription.Environment),
		UserID:      userID, ItemKey: item.ItemKey, ProductID: item.HuaweiProductID,
		SubscriptionKey:   subscriptionKey(order, purchaseToken),
		PurchaseTokenHash: security.SHA256(purchaseToken), PurchaseTokenCiphertext: encryptedToken,
		Status: status, AutoRenewing: int(state.RenewalInfo.AutoRenewStatusCode) == 1,
		StartsAt: startsAt, ExpiresAt: expiresAt,
		RevokedAt: revokedAt(status, order, now), VerifiedAt: now, Source: source,
	}, now)
	if err != nil {
		return err
	}
	if orderResult.NeedsConfirm {
		if err := s.iap.ConfirmSubscription(ctx, order.PurchaseOrderID, purchaseToken); err != nil {
			return fmt.Errorf("%w: %v", ErrHuaweiUnavailable, err)
		}
		if err := s.repository.MarkOrderAcknowledged(ctx, orderResult.OrderDatabaseID, s.now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func revokedAt(status string, order huawei_iap.PurchaseOrderPayload, now time.Time) *time.Time {
	if status != "REVOKED" {
		return nil
	}
	value := order.RevocationTime.Time()
	if value.IsZero() {
		value = now
	}
	return &value
}

func providerEnvironment(value string) string {
	if value == "SANDBOX" {
		return "SANDBOX"
	}
	return "PRODUCTION"
}

func subscriptionStatus(status int, autoRenewStatus int) string {
	switch status {
	case 1:
		if autoRenewStatus == 1 {
			return "ACTIVE"
		}
		return "CANCELED_ACTIVE"
	case 2:
		return "EXPIRED"
	case 3:
		return "EXPIRED"
	case 5:
		return "REVOKED"
	default:
		return "EXPIRED"
	}
}

func subscriptionKey(order huawei_iap.PurchaseOrderPayload, purchaseToken string) string {
	if order.OriginalPurchaseOrderID != "" {
		return order.OriginalPurchaseOrderID
	}
	return hex.EncodeToString(security.SHA256(purchaseToken))
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
