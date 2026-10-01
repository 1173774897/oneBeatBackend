package iap

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// NotificationPayload is the decoded JWS payload for Harmony IAP key event notifications (v3).
type NotificationPayload struct {
	NotificationType      string               `json:"notificationType"`
	NotificationSubtype   string               `json:"notificationSubtype"`
	NotificationRequestID string               `json:"notificationRequestId"`
	NotificationVersion   string               `json:"notificationVersion"`
	SignedTime            Millis               `json:"signedTime"`
	NotificationMetaData  NotificationMetaData `json:"notificationMetaData"`
}

type NotificationMetaData struct {
	Environment          string      `json:"environment"`
	ApplicationID        string      `json:"applicationId"`
	PackageName          string      `json:"packageName"`
	Type                 FlexibleInt `json:"type"`
	CurrentProductID     string      `json:"currentProductId"`
	SubGroupID           string      `json:"subGroupId"`
	SubGroupGenerationID string      `json:"subGroupGenerationId"`
	SubscriptionID       string      `json:"subscriptionId"`
	PurchaseToken        string      `json:"purchaseToken"`
	PurchaseOrderID      string      `json:"purchaseOrderId"`
}

func ExtractJWSNotification(rawBody []byte) (string, error) {
	rawBody = bytesTrimSpace(rawBody)
	if len(rawBody) == 0 {
		return "", errors.New("empty notification body")
	}
	var envelope struct {
		JWSNotification string `json:"jwsNotification"`
	}
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		return "", fmt.Errorf("decode notification envelope: %w", err)
	}
	jws := strings.TrimSpace(envelope.JWSNotification)
	if jws == "" {
		return "", errors.New("missing jwsNotification")
	}
	return jws, nil
}

func (c *Client) VerifyNotification(compact string) (NotificationPayload, error) {
	payloadBytes, err := c.verifyHuaweiJWS(strings.TrimSpace(compact))
	if err != nil {
		return NotificationPayload{}, fmt.Errorf("verify notification JWS: %w", err)
	}
	var payload NotificationPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return NotificationPayload{}, fmt.Errorf("decode notification payload: %w", err)
	}
	if strings.TrimSpace(payload.NotificationRequestID) == "" {
		return NotificationPayload{}, errors.New("notification is missing notificationRequestId")
	}
	return payload, nil
}

// DecodeNotificationUnverified extracts routing metadata so the raw event can be
// persisted before verification. Callers must not mutate business state until
// VerifyNotification succeeds.
func DecodeNotificationUnverified(compact string) (NotificationPayload, error) {
	parts := strings.Split(strings.TrimSpace(compact), ".")
	if len(parts) != 3 {
		return NotificationPayload{}, errors.New("notification JWS must contain three segments")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return NotificationPayload{}, fmt.Errorf("decode unverified notification payload: %w", err)
	}
	var payload NotificationPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return NotificationPayload{}, fmt.Errorf("parse unverified notification payload: %w", err)
	}
	if strings.TrimSpace(payload.NotificationRequestID) == "" {
		return NotificationPayload{}, errors.New("notification is missing notificationRequestId")
	}
	return payload, nil
}

// RedactedNotificationSnapshot removes purchase tokens before the event is persisted.
func RedactedNotificationSnapshot(payload NotificationPayload) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var generic map[string]interface{}
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	redactTokenFields(generic)
	return json.Marshal(generic)
}

func redactTokenFields(value map[string]interface{}) {
	for key, nested := range value {
		switch strings.ToLower(key) {
		case "purchasetoken", "purchaseToken":
			value[key] = "[redacted]"
		case "notificationmetadata", "notificationMetaData":
			if meta, ok := nested.(map[string]interface{}); ok {
				redactTokenFields(meta)
			}
		}
	}
}

func bytesTrimSpace(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}
