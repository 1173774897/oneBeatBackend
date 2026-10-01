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

var (
	ErrWebhookInvalidSignature = errors.New("invalid Huawei notification signature")
	ErrWebhookInvalidPayload   = errors.New("invalid Huawei notification payload")
	ErrWebhookEnvironment      = errors.New("notification environment does not match server")
	ErrWebhookUnknownOrder     = errors.New("notification purchase is not linked to a OneBeat account")
)

func WebhookDBEnvironment(appEnv string) string {
	switch strings.ToLower(strings.TrimSpace(appEnv)) {
	case "production", "prod":
		return "PRODUCTION"
	default:
		return "SANDBOX"
	}
}

func (s *Service) HandleHuaweiIAPWebhook(ctx context.Context, appEnv string, rawBody []byte) error {
	jws, err := huawei_iap.ExtractJWSNotification(rawBody)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWebhookInvalidPayload, err)
	}
	unverified, err := huawei_iap.DecodeNotificationUnverified(jws)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWebhookInvalidPayload, err)
	}
	snapshot, err := huawei_iap.RedactedNotificationSnapshot(unverified)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWebhookInvalidPayload, err)
	}
	notificationType := strings.TrimSpace(unverified.NotificationType)
	if notificationType == "" {
		notificationType = "UNKNOWN"
	}
	environment := WebhookDBEnvironment(appEnv)
	claim, err := s.repository.AcquireWebhookEvent(
		ctx,
		unverified.NotificationRequestID,
		environment,
		notificationType,
		strings.TrimSpace(unverified.NotificationSubtype),
		false,
		snapshot,
	)
	if err != nil {
		return err
	}
	if claim.AlreadyProcessed {
		return nil
	}
	payload, err := s.iap.VerifyNotification(jws)
	if err != nil {
		processErr := fmt.Errorf("%w: %v", ErrWebhookInvalidSignature, err)
		_ = s.repository.FinishWebhookEvent(
			ctx, claim.EventDatabaseID, "FAILED", sanitizeWebhookError(processErr), s.now().UTC(),
		)
		return processErr
	}
	if err := s.repository.MarkWebhookSignatureValid(ctx, claim.EventDatabaseID); err != nil {
		return err
	}

	processErr := s.validateVerifiedNotification(payload)
	if processErr == nil {
		processErr = s.processNotification(ctx, environment, payload)
	}
	finishStatus := "PROCESSED"
	lastError := ""
	if processErr != nil {
		finishStatus = "FAILED"
		lastError = sanitizeWebhookError(processErr)
	}
	if err := s.repository.FinishWebhookEvent(
		ctx, claim.EventDatabaseID, finishStatus, lastError, s.now().UTC(),
	); err != nil {
		return err
	}
	return processErr
}

func (s *Service) validateVerifiedNotification(payload huawei_iap.NotificationPayload) error {
	if err := s.validateNotificationApplication(payload); err != nil {
		return fmt.Errorf("%w: %v", ErrWebhookInvalidPayload, err)
	}
	if !s.iap.NotificationApplicationMatches(payload.NotificationMetaData.ApplicationID) {
		return fmt.Errorf("%w: application mismatch", ErrWebhookInvalidPayload)
	}
	if !s.iap.NotificationEnvironmentMatches(payload.NotificationMetaData.Environment) {
		return ErrWebhookEnvironment
	}
	return nil
}

func (s *Service) validateNotificationApplication(payload huawei_iap.NotificationPayload) error {
	meta := payload.NotificationMetaData
	if strings.TrimSpace(meta.PurchaseToken) == "" {
		return errors.New("notification metadata is missing purchaseToken")
	}
	if strings.TrimSpace(meta.CurrentProductID) == "" && strings.TrimSpace(meta.SubscriptionID) == "" {
		return errors.New("notification metadata is missing product identity")
	}
	return nil
}

func (s *Service) processNotification(
	ctx context.Context,
	environment string,
	payload huawei_iap.NotificationPayload,
) error {
	meta := payload.NotificationMetaData
	productID := strings.TrimSpace(meta.CurrentProductID)
	if productID == "" {
		productID = strings.TrimSpace(meta.SubscriptionID)
	}
	item, itemKnown := catalog.FindByProductID(productID)
	isSubscription := int(meta.Type) == 2 || (itemKnown && item.IAPProductType == catalog.ProductAutoRenewable)
	if isSubscription {
		return s.processSubscriptionNotification(ctx, environment, payload, productID, item, itemKnown)
	}
	return s.processOrderNotification(ctx, environment, payload, productID, item, itemKnown)
}

func (s *Service) processSubscriptionNotification(
	ctx context.Context,
	environment string,
	payload huawei_iap.NotificationPayload,
	productID string,
	item catalog.Item,
	itemKnown bool,
) error {
	if !itemKnown || item.IAPProductType != catalog.ProductAutoRenewable {
		return fmt.Errorf("%w: unknown subscription product %s", ErrInvalidPurchase, productID)
	}
	meta := payload.NotificationMetaData
	preview, err := s.iap.QuerySubscription(ctx, item.HuaweiProductID, meta.PurchaseToken)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrHuaweiUnavailable, err)
	}
	order := preview.LastSubscriptionStatus.LastPurchaseOrder
	lookup, err := s.repository.FindPurchaseOwner(
		ctx,
		environment,
		meta.PurchaseOrderID,
		security.SHA256(meta.PurchaseToken),
		order.DeveloperPayload,
	)
	if err != nil {
		return ErrWebhookUnknownOrder
	}
	if err := s.iap.ValidateSubscription(preview, item.HuaweiProductID, s.DeveloperPayload(lookup.UserID)); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPurchase, err)
	}
	reference := huawei_iap.PurchaseReference{
		PurchaseOrderID: meta.PurchaseOrderID,
		PurchaseToken:   meta.PurchaseToken,
		ProductID:       item.HuaweiProductID,
		ProductType:     2,
	}
	if err := s.verifySubscriptionFromSource(
		ctx, lookup.UserID, item, reference, s.DeveloperPayload(lookup.UserID), "WEBHOOK",
	); err != nil {
		return err
	}

	if isRefundNotification(payload.NotificationType, payload.NotificationSubtype) {
		if err := s.persistWebhookRefund(ctx, environment, lookup, item, payload, preview); err != nil {
			return err
		}
	}
	return s.recordWebhookAudit(ctx, lookup, item, payload)
}

func (s *Service) processOrderNotification(
	ctx context.Context,
	environment string,
	payload huawei_iap.NotificationPayload,
	productID string,
	item catalog.Item,
	itemKnown bool,
) error {
	if !itemKnown {
		return fmt.Errorf("%w: unknown product %s", ErrInvalidPurchase, productID)
	}
	meta := payload.NotificationMetaData
	order, err := s.iap.QueryOrder(ctx, meta.PurchaseOrderID, meta.PurchaseToken)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrHuaweiUnavailable, err)
	}
	lookup, err := s.repository.FindPurchaseOwner(
		ctx,
		environment,
		meta.PurchaseOrderID,
		security.SHA256(meta.PurchaseToken),
		order.DeveloperPayload,
	)
	if err != nil {
		return ErrWebhookUnknownOrder
	}
	if err := s.verifyNonConsumable(ctx, lookup.UserID, item, huawei_iap.PurchaseReference{
		PurchaseOrderID: meta.PurchaseOrderID,
		PurchaseToken:   meta.PurchaseToken,
		ProductID:       item.HuaweiProductID,
		ProductType:     1,
	}, s.DeveloperPayload(lookup.UserID)); err != nil {
		return err
	}
	if isRefundNotification(payload.NotificationType, payload.NotificationSubtype) {
		if err := s.persistOrderWebhookRefund(ctx, environment, lookup, item, payload, order); err != nil {
			return err
		}
	}
	return s.recordWebhookAudit(ctx, lookup, item, payload)
}

func (s *Service) persistOrderWebhookRefund(
	ctx context.Context,
	environment string,
	lookup repository.OrderLookup,
	item catalog.Item,
	payload huawei_iap.NotificationPayload,
	order huawei_iap.PurchaseOrderPayload,
) error {
	meta := payload.NotificationMetaData
	token := order.PurchaseToken
	if token == "" {
		token = meta.PurchaseToken
	}
	ciphertext, err := security.EncryptToken(s.tokenEncryptionKey, token)
	if err != nil {
		return err
	}
	orderID := meta.PurchaseOrderID
	if orderID == "" {
		orderID = order.PurchaseOrderID
	}
	occurredAt := payload.SignedTime.Time()
	if occurredAt.IsZero() {
		occurredAt = s.now().UTC()
	}
	return s.repository.ApplyRefund(ctx, repository.RefundRecord{
		Environment: environment, UserID: lookup.UserID,
		ItemKey: item.ItemKey, ProductID: item.HuaweiProductID,
		ProductType: catalog.ProductNonConsumable, OrderID: orderID,
		PurchaseTokenHash: security.SHA256(token), PurchaseTokenCiphertext: ciphertext,
		DeveloperPayload: s.DeveloperPayload(lookup.UserID), RefundType: "USER_REFUND",
		EntitlementEffect: "REVOKE",
		ProviderStatus:    strings.ToUpper(payload.NotificationType + " " + payload.NotificationSubtype),
		OccurredAt:        occurredAt,
		Receipt:           payload,
	}, s.now().UTC())
}

func (s *Service) persistWebhookRefund(
	ctx context.Context,
	environment string,
	lookup repository.OrderLookup,
	item catalog.Item,
	payload huawei_iap.NotificationPayload,
	snapshot huawei_iap.SubGroupStatusPayload,
) error {
	meta := payload.NotificationMetaData
	state := snapshot.LastSubscriptionStatus
	order := state.LastPurchaseOrder
	status := subscriptionStatus(int(state.Status), int(state.RenewalInfo.AutoRenewStatusCode))
	refundType := "USER_REFUND"
	effect := "PENDING"
	combined := strings.ToUpper(payload.NotificationType + " " + payload.NotificationSubtype)
	if strings.Contains(combined, "REVOKE") {
		refundType = "WITHDRAWAL"
		effect = "REVOKE"
	} else if status == "ACTIVE" || status == "CANCELED_ACTIVE" {
		if state.ExpiresTime.Time().After(s.now().UTC()) {
			effect = "KEEP"
		}
	} else if status == "EXPIRED" || status == "REVOKED" {
		effect = "REVOKE"
	}
	token := state.PurchaseToken
	if token == "" {
		token = meta.PurchaseToken
	}
	ciphertext, err := security.EncryptToken(s.tokenEncryptionKey, token)
	if err != nil {
		return err
	}
	orderID := meta.PurchaseOrderID
	if orderID == "" {
		orderID = order.PurchaseOrderID
	}
	occurredAt := payload.SignedTime.Time()
	if occurredAt.IsZero() {
		occurredAt = s.now().UTC()
	}
	return s.repository.ApplyRefund(ctx, repository.RefundRecord{
		Environment: environment, UserID: lookup.UserID, SubscriptionID: lookup.SubscriptionID,
		ItemKey: item.ItemKey, ProductID: item.HuaweiProductID,
		ProductType: catalog.ProductAutoRenewable, OrderID: orderID,
		PurchaseTokenHash: security.SHA256(token), PurchaseTokenCiphertext: ciphertext,
		DeveloperPayload: s.DeveloperPayload(lookup.UserID), RefundType: refundType,
		EntitlementEffect: effect, ProviderStatus: combined, OccurredAt: occurredAt,
		Receipt: payload,
	}, s.now().UTC())
}

func isRefundNotification(notificationType string, subtype string) bool {
	combined := strings.ToUpper(notificationType + " " + subtype)
	return strings.Contains(combined, "REFUND") || strings.Contains(combined, "REVOKE")
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
		lookup.SubscriptionID,
		payload.NotificationRequestID,
	)
}

func auditEventType(notificationType string, subtype string) string {
	combined := strings.ToUpper(notificationType + " " + subtype)
	switch {
	case strings.Contains(combined, "REFUND"):
		return "REFUNDED"
	case strings.Contains(combined, "REVOKE"):
		return "REVOKED"
	case strings.Contains(combined, "EXPIRE"), strings.Contains(combined, "BILLING_RETRY"):
		return "EXPIRED"
	default:
		return "RESTORED"
	}
}

func sanitizeWebhookError(err error) string {
	message := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(message) > 500 {
		message = message[:500]
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "token") || strings.Contains(lower, "purchase") {
		return "notification processing failed"
	}
	return message
}
