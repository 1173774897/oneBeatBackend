package iap

import "testing"

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
