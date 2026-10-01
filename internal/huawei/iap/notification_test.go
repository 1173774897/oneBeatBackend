package iap

import (
	"encoding/base64"
	"testing"
)

func TestExtractJWSNotification(t *testing.T) {
	raw := []byte(`{"jwsNotification":"header.payload.signature"}`)
	jws, err := ExtractJWSNotification(raw)
	if err != nil {
		t.Fatalf("ExtractJWSNotification: %v", err)
	}
	if jws != "header.payload.signature" {
		t.Fatalf("jws = %q", jws)
	}
}

func TestExtractJWSNotificationMissingField(t *testing.T) {
	if _, err := ExtractJWSNotification([]byte(`{"other":1}`)); err == nil {
		t.Fatal("expected error for missing jwsNotification")
	}
}

func TestDecodeNotificationUnverifiedOnlyExtractsRoutingMetadata(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{
		"notificationRequestId":"event-1",
		"notificationType":"EXPIRE",
		"notificationSubtype":"VOLUNTARY"
	}`))
	decoded, err := DecodeNotificationUnverified("header." + payload + ".signature")
	if err != nil {
		t.Fatalf("DecodeNotificationUnverified: %v", err)
	}
	if decoded.NotificationRequestID != "event-1" || decoded.NotificationSubtype != "VOLUNTARY" {
		t.Fatalf("decoded = %#v", decoded)
	}
}
