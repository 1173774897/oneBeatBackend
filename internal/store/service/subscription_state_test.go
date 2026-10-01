package service

import "testing"

func TestSubscriptionStatusKeepsCanceledPeriodActive(t *testing.T) {
	if got := subscriptionStatus(1, 0); got != "CANCELED_ACTIVE" {
		t.Fatalf("status = %q", got)
	}
	if got := subscriptionStatus(1, 1); got != "ACTIVE" {
		t.Fatalf("status = %q", got)
	}
}

func TestRetryStateNormalizesToExpiredWithoutEntitlement(t *testing.T) {
	for _, providerStatus := range []int{2, 3} {
		if got := subscriptionStatus(providerStatus, 1); got != "EXPIRED" {
			t.Fatalf("provider status %d = %q", providerStatus, got)
		}
	}
	if got := subscriptionStatus(5, 1); got != "REVOKED" {
		t.Fatalf("provider status 5 = %q", got)
	}
}

func TestExpireNotificationMapsToExpiredAudit(t *testing.T) {
	for _, subtype := range []string{"BILLING_RETRY", "VOLUNTARY", "PRODUCT_NOT_FOR_SALE"} {
		if got := auditEventType("EXPIRE", subtype); got != "EXPIRED" {
			t.Fatalf("%s audit event = %q", subtype, got)
		}
	}
}

func TestRenewalStatusNotificationDoesNotLookLikeRefund(t *testing.T) {
	for _, subtype := range []string{"AUTO_RENEW_DISABLED", "AUTO_RENEW_ENABLED"} {
		if isRefundNotification("DID_CHANGE_RENEWAL_STATUS", subtype) {
			t.Fatalf("%s classified as refund", subtype)
		}
	}
}
