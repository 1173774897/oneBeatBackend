package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	huawei_iap "onebeat/store-api/internal/huawei/iap"
	"onebeat/store-api/internal/store/catalog"
	"onebeat/store-api/internal/store/repository"
	"onebeat/store-api/internal/store/security"
)

var (
	ErrInvalidPurchase   = errors.New("purchase verification failed")
	ErrHuaweiUnavailable = errors.New("Huawei IAP is unavailable")
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
	repository            *repository.Repository
	iap                   IAPClient
	purchaseBindingSecret []byte
	tokenEncryptionKey    []byte
	now                   func() time.Time
}

type VerifyInput struct {
	IdempotencyKey string `json:"idempotencyKey"`
	ProductID      string `json:"productId"`
	PurchaseData   string `json:"purchaseData"`
}

func New(
	repository *repository.Repository,
	iap IAPClient,
	purchaseBindingSecret []byte,
	tokenEncryptionKey []byte,
) *Service {
	return &Service{
		repository: repository, iap: iap,
		purchaseBindingSecret: append([]byte(nil), purchaseBindingSecret...),
		tokenEncryptionKey:    append([]byte(nil), tokenEncryptionKey...),
		now:                   time.Now,
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
