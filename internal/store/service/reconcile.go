package service

import (
	"context"

	huawei_iap "onebeat/store-api/internal/huawei/iap"
	"onebeat/store-api/internal/store/catalog"
	"onebeat/store-api/internal/store/security"
)

func (s *Service) ReconcileStoredPurchases(ctx context.Context, userID string) error {
	rows, err := s.repository.ListStoredPurchasesForUser(ctx, userID)
	if err != nil {
		return err
	}
	successCount := 0
	var lastErr error
	for _, row := range rows {
		token, err := security.DecryptToken(s.tokenEncryptionKey, row.PurchaseTokenCiphertext)
		if err != nil || token == "" {
			lastErr = err
			continue
		}
		item, ok := catalog.FindByProductID(row.ProductID)
		if !ok {
			item = catalog.Item{ItemKey: row.ItemKey, HuaweiProductID: row.ProductID, IAPProductType: row.ProductType}
		}
		if item.IAPProductType == "" {
			item.IAPProductType = row.ProductType
		}
		developerPayload := row.DeveloperPayload
		if developerPayload == "" {
			developerPayload = s.DeveloperPayload(userID)
		}
		reference := huawei_iap.PurchaseReference{
			PurchaseOrderID: row.OrderID,
			PurchaseToken:   token,
			ProductID:       row.ProductID,
		}
		if item.IAPProductType == catalog.ProductAutoRenewable {
			reference.ProductType = 2
			err = s.verifySubscription(ctx, userID, item, reference, developerPayload)
		} else {
			reference.ProductType = 1
			err = s.verifyNonConsumable(ctx, userID, item, reference, developerPayload)
		}
		if err != nil {
			lastErr = err
			continue
		}
		successCount++
	}
	if successCount == 0 && lastErr != nil {
		return lastErr
	}
	return nil
}
