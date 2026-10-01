package service

import (
	"errors"
	"testing"

	huawei_iap "onebeat/store-api/internal/huawei/iap"
)

func TestOneTimeNotificationMayOmitProductIdentity(t *testing.T) {
	service := &Service{}
	payload := huawei_iap.NotificationPayload{NotificationMetaData: huawei_iap.NotificationMetaData{
		Type:            1,
		PurchaseToken:   "purchase-token",
		PurchaseOrderID: "purchase-order",
	}}

	if err := service.validateNotificationApplication(payload); err != nil {
		t.Fatalf("one-time notification was rejected: %v", err)
	}
}

func TestSubscriptionNotificationStillRequiresProductIdentity(t *testing.T) {
	service := &Service{}
	payload := huawei_iap.NotificationPayload{NotificationMetaData: huawei_iap.NotificationMetaData{
		Type:            2,
		PurchaseToken:   "purchase-token",
		PurchaseOrderID: "purchase-order",
	}}

	if err := service.validateNotificationApplication(payload); err == nil {
		t.Fatal("subscription notification without product identity was accepted")
	}
}

func TestOneTimeNotificationResolvesProductFromAuthoritativeOrder(t *testing.T) {
	item, err := oneTimeItemFromOrder("", huawei_iap.PurchaseOrderPayload{
		ProductID:   "onebeat.character.cloud",
		ProductType: 1,
	})
	if err != nil {
		t.Fatalf("resolve one-time item: %v", err)
	}
	if item.HuaweiProductID != "onebeat.character.cloud" {
		t.Fatalf("product = %q", item.HuaweiProductID)
	}
}

func TestOneTimeNotificationRejectsProductMismatch(t *testing.T) {
	_, err := oneTimeItemFromOrder("onebeat.character.teapot", huawei_iap.PurchaseOrderPayload{
		ProductID:   "onebeat.character.cloud",
		ProductType: 1,
	})
	if !errors.Is(err, ErrInvalidPurchase) {
		t.Fatalf("error = %v, want ErrInvalidPurchase", err)
	}
}
