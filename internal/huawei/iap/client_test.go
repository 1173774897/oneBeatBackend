package iap

import "testing"

func TestValidateOrderIgnoresDeveloperPayloadMismatchInSandbox(t *testing.T) {
	client := &Client{
		environment: "sandbox", applicationID: "app-id", packageName: "com.example.app",
	}
	order := PurchaseOrderPayload{
		ApplicationID: "app-id", PackageName: "com.example.app",
		ProductID: "onebeat.character.cloud", ProductType: 1,
		PurchaseOrderID: "order-id", PurchaseToken: "token",
		DeveloperPayload: "legacy-binding", Environment: "SANDBOX",
	}
	if err := client.ValidateOrder(order, "onebeat.character.cloud", 1, "expected-binding"); err != nil {
		t.Fatalf("validate sandbox legacy order: %v", err)
	}
}

func TestValidateOrderRejectsMismatchedDeveloperPayloadInProduction(t *testing.T) {
	client := &Client{
		environment: "production", applicationID: "app-id", packageName: "com.example.app",
	}
	order := PurchaseOrderPayload{
		ApplicationID: "app-id", PackageName: "com.example.app",
		ProductID: "onebeat.character.cloud", ProductType: 1,
		PurchaseOrderID: "order-id", PurchaseToken: "token",
		DeveloperPayload: "other-binding", Environment: "NORMAL",
	}
	if err := client.ValidateOrder(order, "onebeat.character.cloud", 1, "expected-binding"); err == nil {
		t.Fatal("expected binding mismatch in production")
	}
}

func TestValidateSubscriptionUsesTopLevelEnvironmentAndStateToken(t *testing.T) {
	client := &Client{
		environment: "sandbox", applicationID: "app-id", packageName: "com.example.app",
	}
	subscription := SubGroupStatusPayload{
		Environment: "SANDBOX",
		LastSubscriptionStatus: SubscriptionStatus{
			PurchaseToken: "state-token",
			LastPurchaseOrder: PurchaseOrderPayload{
				ApplicationID: "app-id", PackageName: "com.example.app",
				ProductID: "monthly", ProductType: 2, PurchaseOrderID: "order-id",
				DeveloperPayload: "binding",
			},
			RenewalInfo: RenewalInfo{ProductID: "monthly"},
		},
	}
	if err := client.ValidateSubscription(subscription, "monthly", "binding"); err != nil {
		t.Fatalf("validate subscription: %v", err)
	}
}
