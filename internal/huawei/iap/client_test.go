package iap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

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

func TestQuerySubscriptionSendsPurchaseOrderIDAndToken(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	client := &Client{
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"responseCode":"1","responseMessage":"fixture"}`)),
				Header:     make(http.Header),
			}, nil
		})},
		signingKey: key,
		keyID:      "key-id", issuerID: "issuer-id", applicationID: "app-id",
		now: func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
	_, _ = client.QuerySubscription(context.Background(), "order-abc", "token-1")
	if body["purchaseOrderId"] != "order-abc" {
		t.Fatalf("purchaseOrderId = %q", body["purchaseOrderId"])
	}
	if body["purchaseToken"] != "token-1" {
		t.Fatalf("purchaseToken = %q", body["purchaseToken"])
	}
}
