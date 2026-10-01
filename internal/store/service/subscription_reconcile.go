package service

import (
	"context"

	huawei_iap "onebeat/store-api/internal/huawei/iap"
	"onebeat/store-api/internal/store/catalog"
)

func (s *Service) reconcileSubscription(
	ctx context.Context,
	userID string,
	item catalog.Item,
	storedOrderID string,
	storedToken string,
	developerPayload string,
) error {
	return s.verifySubscriptionFromSource(ctx, userID, item, huawei_iap.PurchaseReference{
		PurchaseOrderID: storedOrderID,
		PurchaseToken:   storedToken,
		ProductID:       item.HuaweiProductID,
		ProductType:     2,
	}, developerPayload, "RECONCILIATION")
}
