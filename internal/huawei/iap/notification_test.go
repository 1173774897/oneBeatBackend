package iap

import "testing"

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
