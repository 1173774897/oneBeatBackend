package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	huawei_iap "onebeat/store-api/internal/huawei/iap"
	"onebeat/store-api/internal/store/catalog"
	"onebeat/store-api/internal/store/repository"
	"onebeat/store-api/internal/store/security"
)

func (s *Service) ProcessTradeOrder(ctx context.Context, trade huawei_iap.TradeOrder) error {
	orderID := trade.EffectiveOrderID()
	productID := trade.EffectiveProductID()
	tradeType := strings.ToUpper(strings.TrimSpace(trade.TradeType))
	if orderID == "" || productID == "" || trade.PurchaseToken == "" {
		return errors.New("reconciliation trade is missing order, product, or token")
	}
	item, ok := catalog.FindByProductID(productID)
	if !ok {
		return fmt.Errorf("unknown reconciliation product %s", productID)
	}
	environment := providerEnvironment(trade.Environment)
	if trade.Environment == "" {
		environment = "PRODUCTION"
	}

	lookup, err := s.repository.FindPurchaseOwner(
		ctx,
		environment,
		orderID,
		security.SHA256(trade.PurchaseToken),
		trade.DeveloperPayload,
	)
	if err != nil {
		return ErrWebhookUnknownOrder
	}
	developerPayload := s.DeveloperPayload(lookup.UserID)
	if tradeType == "PURCHASE" {
		reference := huawei_iap.PurchaseReference{
			PurchaseOrderID: orderID,
			PurchaseToken:   trade.PurchaseToken,
			ProductID:       productID,
			ProductType:     int(trade.ProductType),
		}
		if item.IAPProductType == catalog.ProductAutoRenewable {
			return s.verifySubscriptionFromSource(
				ctx, lookup.UserID, item, reference, developerPayload, "RECONCILIATION",
			)
		}
		return s.verifyNonConsumable(ctx, lookup.UserID, item, reference, developerPayload)
	}
	if tradeType != "REFUND" {
		return fmt.Errorf("unsupported reconciliation trade type %s", tradeType)
	}

	effect := "REVOKE"
	refundType := "UNKNOWN"
	if item.IAPProductType == catalog.ProductAutoRenewable {
		snapshot, err := s.iap.QuerySubscription(ctx, item.HuaweiProductID, trade.PurchaseToken)
		if err != nil {
			effect = "PENDING"
		} else {
			state := snapshot.LastSubscriptionStatus
			status := subscriptionStatus(int(state.Status), int(state.RenewalInfo.AutoRenewStatusCode))
			if (status == "ACTIVE" || status == "CANCELED_ACTIVE") &&
				state.ExpiresTime.Time().After(s.now().UTC()) {
				effect = "KEEP"
			}
			if err := s.verifySubscriptionFromSource(ctx, lookup.UserID, item, huawei_iap.PurchaseReference{
				PurchaseOrderID: orderID,
				PurchaseToken:   trade.PurchaseToken,
				ProductID:       item.HuaweiProductID,
				ProductType:     2,
			}, developerPayload, "RECONCILIATION"); err != nil {
				return err
			}
		}
	}
	ciphertext, err := security.EncryptToken(s.tokenEncryptionKey, trade.PurchaseToken)
	if err != nil {
		return err
	}
	occurredAt := trade.OccurredAt()
	if occurredAt.IsZero() {
		occurredAt = s.now().UTC()
	}
	return s.repository.ApplyRefund(ctx, repository.RefundRecord{
		Environment: environment, UserID: lookup.UserID, SubscriptionID: lookup.SubscriptionID,
		ItemKey: item.ItemKey, ProductID: item.HuaweiProductID, ProductType: item.IAPProductType,
		OrderID: orderID, PurchaseTokenHash: security.SHA256(trade.PurchaseToken),
		PurchaseTokenCiphertext: ciphertext, DeveloperPayload: developerPayload,
		RefundType: refundType, EntitlementEffect: effect, ProviderStatus: "REFUND",
		OccurredAt: occurredAt, Receipt: trade,
	}, s.now().UTC())
}
