package service

import (
	"context"
	"fmt"

	"onebeat/store-api/internal/store/catalog"
	"onebeat/store-api/internal/store/repository"
	"onebeat/store-api/internal/store/security"
)

func (s *Service) reconcileNonConsumable(
	ctx context.Context,
	userID string,
	item catalog.Item,
	storedOrderID string,
	storedToken string,
	developerPayload string,
) error {
	order, err := s.iap.QueryOrder(ctx, storedOrderID, storedToken)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrHuaweiUnavailable, err)
	}
	if err := s.iap.ValidateOrder(order, item.HuaweiProductID, 1, developerPayload); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPurchase, err)
	}
	persistOrderID := order.PurchaseOrderID
	if persistOrderID == "" {
		persistOrderID = storedOrderID
	}
	token := order.PurchaseToken
	if token == "" {
		token = storedToken
	}
	encryptedToken, err := security.EncryptToken(s.tokenEncryptionKey, token)
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
		UserID: userID, ItemKey: item.ItemKey, ProductID: item.HuaweiProductID,
		ProductType: catalog.ProductNonConsumable, OrderID: persistOrderID,
		OriginalOrderID:   optionalString(order.OriginalPurchaseOrderID),
		PurchaseTokenHash: security.SHA256(token), PurchaseTokenCiphertext: encryptedToken,
		DeveloperPayload: developerPayload, Status: status, PurchasedAt: &purchasedAt,
		Finished: order.FinishStatus == "FINISHED", Receipt: order,
	}, now)
	if err != nil {
		return err
	}
	if persistOrderID != storedOrderID && storedOrderID != "" {
		_ = s.repository.MarkHuaweiOrderStatus(ctx, userID, storedOrderID, status, now)
	}
	if result.NeedsConfirm && status == "PURCHASED" {
		if err := s.iap.ConfirmOrder(ctx, persistOrderID, token); err != nil {
			return fmt.Errorf("%w: %v", ErrHuaweiUnavailable, err)
		}
		if err := s.repository.MarkOrderAcknowledged(ctx, result.OrderDatabaseID, s.now().UTC()); err != nil {
			return err
		}
	}
	return nil
}
