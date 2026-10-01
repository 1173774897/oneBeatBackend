package service

import (
	"context"
	"fmt"
	"time"

	huawei_iap "onebeat/store-api/internal/huawei/iap"
	"onebeat/store-api/internal/store/catalog"
	"onebeat/store-api/internal/store/repository"
	"onebeat/store-api/internal/store/security"
)

func (s *Service) reconcileSubscription(
	ctx context.Context,
	userID string,
	item catalog.Item,
	storedOrderID string,
	storedToken string,
	developerPayload string,
) error {
	subscription, subErr := s.iap.QuerySubscription(ctx, storedOrderID, storedToken)
	orderSnapshot, orderErr := s.iap.QueryOrder(ctx, storedOrderID, storedToken)
	forcedRevoked := orderErr == nil && orderSnapshot.Revoked()

	if subErr != nil {
		if forcedRevoked {
			return s.applyRevokedAutoRenewableOrder(ctx, userID, item, storedOrderID, storedToken, orderSnapshot, developerPayload)
		}
		return fmt.Errorf("%w: %v", ErrHuaweiUnavailable, subErr)
	}
	if err := s.iap.ValidateSubscription(subscription, item.HuaweiProductID, developerPayload); err != nil {
		if forcedRevoked {
			return s.applyRevokedAutoRenewableOrder(ctx, userID, item, storedOrderID, storedToken, orderSnapshot, developerPayload)
		}
		return fmt.Errorf("%w: %v", ErrInvalidPurchase, err)
	}
	return s.persistAuthoritativeSubscription(
		ctx, userID, item, storedOrderID, storedToken, subscription, developerPayload, forcedRevoked, orderSnapshot,
	)
}

func (s *Service) persistAuthoritativeSubscription(
	ctx context.Context,
	userID string,
	item catalog.Item,
	storedOrderID string,
	storedToken string,
	subscription huawei_iap.SubGroupStatusPayload,
	developerPayload string,
	forcedRevoked bool,
	orderSnapshot huawei_iap.PurchaseOrderPayload,
) error {
	state := subscription.LastSubscriptionStatus
	order := state.LastPurchaseOrder
	if forcedRevoked && orderSnapshot.PurchaseOrderID != "" {
		order = orderSnapshot
	}
	purchaseToken := state.PurchaseToken
	if purchaseToken == "" {
		purchaseToken = order.PurchaseToken
	}
	if purchaseToken == "" {
		purchaseToken = storedToken
	}
	now := s.now().UTC()
	startsAt := order.PurchaseTime.Time()
	expiresAt := state.ExpiresTime.Time()
	if startsAt.IsZero() {
		startsAt = now
	}
	if expiresAt.IsZero() || !expiresAt.After(startsAt) {
		if !order.RevocationTime.Time().IsZero() {
			expiresAt = order.RevocationTime.Time()
		} else {
			expiresAt = now
		}
		if !expiresAt.After(startsAt) {
			expiresAt = startsAt.Add(time.Second)
		}
	}
	status := subscriptionStatus(int(state.Status), int(state.RenewalInfo.AutoRenewStatusCode))
	if forcedRevoked || order.Revoked() {
		status = "REVOKED"
	} else if status == "EXPIRED" || !expiresAt.After(now) {
		status = "EXPIRED"
	}
	orderStatus := iapOrderStatusFromSubscription(order, status, expiresAt, now, forcedRevoked)
	persistOrderID := order.PurchaseOrderID
	if persistOrderID == "" {
		persistOrderID = storedOrderID
	}
	if persistOrderID != storedOrderID && storedOrderID != "" {
		_ = s.repository.MarkHuaweiOrderStatus(ctx, userID, storedOrderID, "REVOKED", now)
	}
	encryptedToken, err := security.EncryptToken(s.tokenEncryptionKey, purchaseToken)
	if err != nil {
		return err
	}
	finished := order.FinishStatus == "FINISHED"
	orderResult, err := s.repository.ApplySubscription(ctx, repository.OrderRecord{
		UserID: userID, ItemKey: item.ItemKey, ProductID: item.HuaweiProductID,
		ProductType: catalog.ProductAutoRenewable, OrderID: persistOrderID,
		OriginalOrderID:   optionalString(order.OriginalPurchaseOrderID),
		PurchaseTokenHash: security.SHA256(purchaseToken), PurchaseTokenCiphertext: encryptedToken,
		DeveloperPayload: developerPayload, Status: orderStatus, PurchasedAt: &startsAt,
		ExpiresAt: &expiresAt, Finished: finished, Receipt: subscription,
	}, repository.SubscriptionRecord{
		UserID: userID, ItemKey: item.ItemKey, ProductID: item.HuaweiProductID,
		SubscriptionKey:   subscriptionKey(order, purchaseToken),
		PurchaseTokenHash: security.SHA256(purchaseToken), PurchaseTokenCiphertext: encryptedToken,
		Status: status, AutoRenewing: !forcedRevoked && int(state.RenewalInfo.AutoRenewStatusCode) == 1,
		StartsAt: startsAt, ExpiresAt: expiresAt, VerifiedAt: now,
	}, now)
	if err != nil {
		return err
	}
	if orderResult.NeedsConfirm && status != "REVOKED" && status != "EXPIRED" {
		if err := s.iap.ConfirmSubscription(ctx, persistOrderID, purchaseToken); err != nil {
			return fmt.Errorf("%w: %v", ErrHuaweiUnavailable, err)
		}
		if err := s.repository.MarkOrderAcknowledged(ctx, orderResult.OrderDatabaseID, s.now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyRevokedAutoRenewableOrder(
	ctx context.Context,
	userID string,
	item catalog.Item,
	storedOrderID string,
	storedToken string,
	order huawei_iap.PurchaseOrderPayload,
	developerPayload string,
) error {
	if err := s.iap.ValidateOrder(order, item.HuaweiProductID, 2, developerPayload); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPurchase, err)
	}
	now := s.now().UTC()
	startsAt := order.PurchaseTime.Time()
	if startsAt.IsZero() {
		startsAt = now
	}
	expiresAt := order.RevocationTime.Time()
	if expiresAt.IsZero() {
		expiresAt = now
	}
	if !expiresAt.After(startsAt) {
		expiresAt = startsAt.Add(time.Second)
	}
	token := order.PurchaseToken
	if token == "" {
		token = storedToken
	}
	encryptedToken, err := security.EncryptToken(s.tokenEncryptionKey, token)
	if err != nil {
		return err
	}
	persistOrderID := order.PurchaseOrderID
	if persistOrderID == "" {
		persistOrderID = storedOrderID
	}
	receipt := map[string]interface{}{
		"source":              "order_status_query",
		"purchaseOrderId":     persistOrderID,
		"revocationReasonSet": order.Revoked(),
	}
	_, err = s.repository.ApplySubscription(ctx, repository.OrderRecord{
		UserID: userID, ItemKey: item.ItemKey, ProductID: item.HuaweiProductID,
		ProductType: catalog.ProductAutoRenewable, OrderID: persistOrderID,
		OriginalOrderID:   optionalString(order.OriginalPurchaseOrderID),
		PurchaseTokenHash: security.SHA256(token), PurchaseTokenCiphertext: encryptedToken,
		DeveloperPayload: developerPayload, Status: "REVOKED", PurchasedAt: &startsAt,
		ExpiresAt: &expiresAt, Finished: order.FinishStatus == "FINISHED", Receipt: receipt,
	}, repository.SubscriptionRecord{
		UserID: userID, ItemKey: item.ItemKey, ProductID: item.HuaweiProductID,
		SubscriptionKey:   subscriptionKey(order, token),
		PurchaseTokenHash: security.SHA256(token), PurchaseTokenCiphertext: encryptedToken,
		Status: "REVOKED", AutoRenewing: false,
		StartsAt: startsAt, ExpiresAt: expiresAt, VerifiedAt: now,
	}, now)
	return err
}

func iapOrderStatusFromSubscription(
	order huawei_iap.PurchaseOrderPayload,
	subscriptionStatus string,
	expiresAt time.Time,
	now time.Time,
	forcedRevoked bool,
) string {
	if forcedRevoked || order.Revoked() || subscriptionStatus == "REVOKED" {
		return "REVOKED"
	}
	if subscriptionStatus == "EXPIRED" || !expiresAt.After(now) {
		return "REFUNDED"
	}
	return "PURCHASED"
}
