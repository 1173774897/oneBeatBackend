package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	huawei_iap "onebeat/store-api/internal/huawei/iap"
	"onebeat/store-api/internal/store/catalog"
	"onebeat/store-api/internal/store/repository"
)

var (
	ErrWebhookInvalidSignature = errors.New("invalid Huawei notification signature")
	ErrWebhookInvalidPayload   = errors.New("invalid Huawei notification payload")
	ErrWebhookEnvironment      = errors.New("notification environment does not match server")
	ErrWebhookUnknownOrder     = errors.New("notification order is not linked to a OneBeat account")
)

func WebhookDBEnvironment(appEnv string) string {
	switch strings.ToLower(strings.TrimSpace(appEnv)) {
	case "production", "prod":
		return "prod"
	default:
		return "test"
	}
}

func (s *Service) HandleHuaweiIAPWebhook(ctx context.Context, appEnv string, rawBody []byte) error {
	jws, err := huawei_iap.ExtractJWSNotification(rawBody)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWebhookInvalidPayload, err)
	}
	payload, err := s.iap.VerifyNotification(jws)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWebhookInvalidSignature, err)
	}
	if err := s.validateNotificationApplication(payload); err != nil {
		return fmt.Errorf("%w: %v", ErrWebhookInvalidPayload, err)
	}
	if !s.iap.NotificationApplicationMatches(payload.NotificationMetaData.ApplicationID) {
		return fmt.Errorf("%w: application mismatch", ErrWebhookInvalidPayload)
	}
	if !s.iap.NotificationEnvironmentMatches(payload.NotificationMetaData.Environment) {
		return ErrWebhookEnvironment
	}

	snapshot, err := huawei_iap.RedactedNotificationSnapshot(payload)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWebhookInvalidPayload, err)
	}
	eventType := strings.TrimSpace(payload.NotificationType)
	if eventType == "" {
		eventType = "UNKNOWN"
	}
	claim, err := s.repository.AcquireWebhookEvent(
		ctx,
		payload.NotificationRequestID,
		WebhookDBEnvironment(appEnv),
		eventType,
		true,
		snapshot,
	)
	if err != nil {
		return err
	}
	if claim.AlreadyProcessed {
		return nil
	}

	processErr := s.processNotification(ctx, payload)
	finishStatus := "PROCESSED"
	var lastError string
	if processErr != nil {
		if errors.Is(processErr, ErrWebhookUnknownOrder) {
			lastError = "order not linked"
		} else {
			finishStatus = "FAILED"
			lastError = sanitizeWebhookError(processErr)
		}
	}
	if err := s.repository.FinishWebhookEvent(ctx, claim.EventDatabaseID, finishStatus, lastError, s.now().UTC()); err != nil {
		return err
	}
	if processErr != nil && !errors.Is(processErr, ErrWebhookUnknownOrder) {
		return processErr
	}
	return nil
}

func (s *Service) validateNotificationApplication(payload huawei_iap.NotificationPayload) error {
	meta := payload.NotificationMetaData
	if strings.TrimSpace(meta.PurchaseOrderID) == "" || strings.TrimSpace(meta.PurchaseToken) == "" {
		return errors.New("notification metadata is missing order identity")
	}
	return nil
}

func (s *Service) processNotification(ctx context.Context, payload huawei_iap.NotificationPayload) error {
	meta := payload.NotificationMetaData
	lookup, err := s.repository.FindOrderByHuaweiOrderID(ctx, meta.PurchaseOrderID)
	if err != nil {
		return ErrWebhookUnknownOrder
	}
	productID := lookup.ProductID
	if meta.CurrentProductID != "" {
		productID = meta.CurrentProductID
	}
	item, ok := catalog.FindByProductID(productID)
	if !ok {
		return fmt.Errorf("%w: unknown product %s", ErrInvalidPurchase, productID)
	}
	developerPayload := lookup.DeveloperPayload
	if developerPayload == "" {
		developerPayload = s.DeveloperPayload(lookup.UserID)
	}
	reference := huawei_iap.PurchaseReference{
		PurchaseOrderID: meta.PurchaseOrderID,
		PurchaseToken:   meta.PurchaseToken,
		ProductID:       productID,
		ProductType:     int(meta.Type),
	}
	if reference.ProductType <= 0 {
		if lookup.ProductType == catalog.ProductAutoRenewable || item.IAPProductType == catalog.ProductAutoRenewable {
			reference.ProductType = 2
		} else {
			reference.ProductType = 1
		}
	}

	var reconcileErr error
	if item.IAPProductType == catalog.ProductAutoRenewable || reference.ProductType == 2 {
		reconcileErr = s.verifySubscription(ctx, lookup.UserID, item, reference, developerPayload)
	} else {
		reconcileErr = s.verifyNonConsumable(ctx, lookup.UserID, item, reference, developerPayload)
	}
	if reconcileErr != nil {
		return reconcileErr
	}
	return s.recordWebhookAudit(ctx, lookup, item, payload)
}

func (s *Service) recordWebhookAudit(
	ctx context.Context,
	lookup repository.OrderLookup,
	item catalog.Item,
	payload huawei_iap.NotificationPayload,
) error {
	eventType := auditEventType(payload.NotificationType, payload.NotificationSubtype)
	sourceType := "IAP_NONCONSUMABLE"
	entitlementKey := item.ItemKey
	if item.IAPProductType == catalog.ProductAutoRenewable {
		sourceType = "IAP_SUBSCRIPTION"
		entitlementKey = "pass.all"
	}
	return s.repository.InsertEntitlementAudit(
		ctx,
		lookup.UserID,
		entitlementKey,
		eventType,
		sourceType,
		nil,
		payload.NotificationRequestID,
	)
}

func auditEventType(notificationType string, subtype string) string {
	combined := strings.ToUpper(notificationType + " " + subtype)
	switch {
	case strings.Contains(combined, "REFUND"):
		return "REFUNDED"
	case strings.Contains(combined, "REVOKE"), strings.Contains(combined, "CANCEL"), strings.Contains(combined, "EXPIRE"):
		return "REVOKED"
	default:
		return "RESTORED"
	}
}

func sanitizeWebhookError(err error) string {
	message := err.Error()
	message = strings.ReplaceAll(message, "\n", " ")
	if len(message) > 500 {
		message = message[:500]
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "token") || strings.Contains(lower, "purchase") {
		return "notification processing failed"
	}
	return message
}
