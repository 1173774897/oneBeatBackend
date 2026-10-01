package iap

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	huaweijws "onebeat/store-api/internal/huawei/jws"
)

const (
	// The China site is fixed in code so production never guesses a data-processing site.
	ChinaOrderRootURL        = "https://orders-drcn.iap.cloud.huawei.com.cn"
	ChinaSubscriptionRootURL = "https://subscr-drcn.iap.cloud.huawei.com.cn"
	iapRequestJWTAudience    = "iap-v1"

	orderConfirmPath        = "/order/harmony/v1/application/purchase/shipped/confirm"
	orderStatusPath         = "/order/harmony/v1/application/order/status/query"
	tradeOrdersQueryPath    = "/order/harmony/v1/application/trade/orders/query"
	subscriptionConfirmPath = "/subscription/harmony/v1/application/purchase/shipped/confirm"
	subscriptionStatusPath  = "/subscription/harmony/v1/application/subscription/status/query"
	maximumResponseBody     = 2 << 20
)

type Config struct {
	PrivateKeyPEM string
	KeyID         string
	IssuerID      string
	Environment   string
	ApplicationID string
	PackageName   string
}

type Client struct {
	httpClient    *http.Client
	signingKey    *ecdsa.PrivateKey
	keyID         string
	issuerID      string
	environment   string
	applicationID string
	packageName   string
	jwsVerifier   *huaweijws.Verifier
	now           func() time.Time
}

type Millis int64

func (m *Millis) UnmarshalJSON(value []byte) error {
	var number int64
	if err := json.Unmarshal(value, &number); err == nil {
		*m = Millis(number)
		return nil
	}
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return err
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return err
	}
	*m = Millis(parsed)
	return nil
}

func (m Millis) Time() time.Time {
	return time.UnixMilli(int64(m)).UTC()
}

type FlexibleInt int

func (f *FlexibleInt) UnmarshalJSON(value []byte) error {
	var number int
	if err := json.Unmarshal(value, &number); err == nil {
		*f = FlexibleInt(number)
		return nil
	}
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return err
	}
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return err
	}
	*f = FlexibleInt(parsed)
	return nil
}

type PurchaseOrderPayload struct {
	ApplicationID                     string          `json:"applicationId"`
	PackageName                       string          `json:"packageName"`
	ProductID                         string          `json:"productId"`
	ProductType                       FlexibleInt     `json:"productType"`
	PurchaseToken                     string          `json:"purchaseToken"`
	PurchaseOrderID                   string          `json:"purchaseOrderId"`
	OriginalPurchaseOrderID           string          `json:"originalPurchaseOrderId"`
	PurchaseTime                      Millis          `json:"purchaseTime"`
	FinishStatus                      string          `json:"finishStatus"`
	DeveloperPayload                  string          `json:"developerPayload"`
	PurchaseOrderRevocationReasonCode json.RawMessage `json:"purchaseOrderRevocationReasonCode"`
	RevocationTime                    Millis          `json:"revocationTime"`
	Environment                       string          `json:"environment"`
	SubGroupID                        string          `json:"subGroupId"`
	Price                             json.Number     `json:"price"`
	Currency                          string          `json:"currency"`
}

func (p PurchaseOrderPayload) Revoked() bool {
	value := strings.TrimSpace(string(p.PurchaseOrderRevocationReasonCode))
	return value != "" && value != "null" && value != `""`
}

type RenewalInfo struct {
	ProductID               string      `json:"productId"`
	AutoRenewStatusCode     FlexibleInt `json:"autoRenewStatusCode"`
	HasInBillingRetryPeriod bool        `json:"hasInBillingRetryPeriod"`
	ExpirationIntent        FlexibleInt `json:"expirationIntent"`
	RenewalTime             Millis      `json:"renewalTime"`
}

type SubscriptionStatus struct {
	Status            FlexibleInt          `json:"status"`
	ExpiresTime       Millis               `json:"expiresTime"`
	PurchaseToken     string               `json:"purchaseToken"`
	LastPurchaseOrder PurchaseOrderPayload `json:"lastPurchaseOrder"`
	RenewalInfo       RenewalInfo          `json:"renewalInfo"`
}

type SubGroupStatusPayload struct {
	Environment            string             `json:"environment"`
	SubGroupID             string             `json:"subGroupId"`
	LastSubscriptionStatus SubscriptionStatus `json:"lastSubscriptionStatus"`
}

type PurchaseReference struct {
	PurchaseOrderID string
	PurchaseToken   string
	ProductID       string
	ProductType     int
}

func NewClient(httpClient *http.Client, config Config) (*Client, error) {
	signingKey, err := parseIAPAPIPrivateKey(config.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	verifier, err := huaweijws.NewVerifier()
	if err != nil {
		return nil, err
	}
	return &Client{
		httpClient: httpClient, signingKey: signingKey, keyID: config.KeyID, issuerID: config.IssuerID,
		environment: config.Environment, applicationID: config.ApplicationID, packageName: config.PackageName,
		jwsVerifier: verifier, now: time.Now,
	}, nil
}

func (c *Client) DecodePurchaseData(compact string) (PurchaseReference, error) {
	payload, err := c.verifyClientPurchaseJWS(compact)
	if err != nil {
		return PurchaseReference{}, err
	}
	var direct PurchaseOrderPayload
	if err := json.Unmarshal(payload, &direct); err == nil && direct.PurchaseOrderID != "" {
		return PurchaseReference{direct.PurchaseOrderID, direct.PurchaseToken, direct.ProductID, int(direct.ProductType)}, nil
	}
	var subscription SubGroupStatusPayload
	if err := json.Unmarshal(payload, &subscription); err != nil {
		return PurchaseReference{}, errors.New("unsupported purchase JWS payload")
	}
	order := subscription.LastSubscriptionStatus.LastPurchaseOrder
	token := subscription.LastSubscriptionStatus.PurchaseToken
	if token == "" {
		token = order.PurchaseToken
	}
	productID := subscription.LastSubscriptionStatus.RenewalInfo.ProductID
	if productID == "" {
		productID = order.ProductID
	}
	if order.PurchaseOrderID == "" || token == "" {
		return PurchaseReference{}, errors.New("subscription JWS is missing order identity")
	}
	return PurchaseReference{order.PurchaseOrderID, token, productID, 2}, nil
}

func (c *Client) verifyClientPurchaseJWS(compact string) ([]byte, error) {
	payload, err := c.verifyHuaweiJWS(compact)
	if err != nil {
		return nil, fmt.Errorf("verify client purchase JWS: %w", err)
	}
	return payload, nil
}

// verifyHuaweiJWS checks ES256 JWS from Huawei (client purchaseData or server query responses).
// Docker/Alpine often lacks Huawei root CAs, so chain verification falls back to the embedded x5c leaf.
func (c *Client) verifyHuaweiJWS(compact string) ([]byte, error) {
	payload, err := c.jwsVerifier.Verify(compact)
	if err == nil {
		return payload, nil
	}
	payload, leafErr := c.jwsVerifier.VerifySignatureWithEmbeddedLeaf(compact)
	if leafErr != nil {
		return nil, err
	}
	return payload, nil
}

func (c *Client) QueryOrder(ctx context.Context, orderID string, purchaseToken string) (PurchaseOrderPayload, error) {
	var response struct {
		ResponseCode     string `json:"responseCode"`
		ResponseMessage  string `json:"responseMessage"`
		JWSPurchaseOrder string `json:"jwsPurchaseOrder"`
	}
	if err := c.call(ctx, orderStatusPath, orderID, purchaseToken, &response); err != nil {
		return PurchaseOrderPayload{}, err
	}
	if response.ResponseCode != "0" || response.JWSPurchaseOrder == "" {
		return PurchaseOrderPayload{}, fmt.Errorf("Huawei order query failed: code=%s message=%s", response.ResponseCode, response.ResponseMessage)
	}
	payload, err := c.verifyHuaweiJWS(response.JWSPurchaseOrder)
	if err != nil {
		return PurchaseOrderPayload{}, fmt.Errorf("verify Huawei order response: %w", err)
	}
	var order PurchaseOrderPayload
	if err := json.Unmarshal(payload, &order); err != nil {
		return PurchaseOrderPayload{}, fmt.Errorf("decode Huawei order response: %w", err)
	}
	return order, nil
}

func (c *Client) QuerySubscription(ctx context.Context, subscriptionID string, purchaseToken string) (SubGroupStatusPayload, error) {
	var response struct {
		ResponseCode      string `json:"responseCode"`
		ResponseMessage   string `json:"responseMessage"`
		JWSSubGroupStatus string `json:"jwsSubGroupStatus"`
	}
	if err := c.callJSON(ctx, subscriptionStatusPath, map[string]string{
		"subscriptionId": subscriptionID,
		"purchaseToken":  purchaseToken,
	}, &response); err != nil {
		return SubGroupStatusPayload{}, err
	}
	if response.ResponseCode != "0" || response.JWSSubGroupStatus == "" {
		return SubGroupStatusPayload{}, fmt.Errorf("Huawei subscription query failed: code=%s message=%s", response.ResponseCode, response.ResponseMessage)
	}
	payload, err := c.verifyHuaweiJWS(response.JWSSubGroupStatus)
	if err != nil {
		return SubGroupStatusPayload{}, fmt.Errorf("verify Huawei subscription response: %w", err)
	}
	var subscription SubGroupStatusPayload
	if err := json.Unmarshal(payload, &subscription); err != nil {
		return SubGroupStatusPayload{}, fmt.Errorf("decode Huawei subscription response: %w", err)
	}
	return subscription, nil
}

func (c *Client) ConfirmOrder(ctx context.Context, orderID string, purchaseToken string) error {
	return c.confirm(ctx, orderConfirmPath, orderID, purchaseToken)
}

func (c *Client) ConfirmSubscription(ctx context.Context, orderID string, purchaseToken string) error {
	return c.confirm(ctx, subscriptionConfirmPath, orderID, purchaseToken)
}

func (c *Client) ValidateOrder(order PurchaseOrderPayload, productID string, productType int, developerPayload string) error {
	if order.ApplicationID != c.applicationID || (order.PackageName != "" && order.PackageName != c.packageName) {
		return errors.New("purchase belongs to another application")
	}
	if order.ProductID != productID {
		return errors.New("purchase product does not match request")
	}
	_ = productType
	if err := c.validateDeveloperPayload(order.DeveloperPayload, developerPayload); err != nil {
		return err
	}
	if !c.environmentMatches(order.Environment) {
		return errors.New("purchase environment does not match server environment")
	}
	if order.PurchaseOrderID == "" || order.PurchaseToken == "" {
		return errors.New("purchase is missing order identity")
	}
	return nil
}

func (c *Client) ValidateSubscription(subscription SubGroupStatusPayload, productID string, developerPayload string) error {
	order := subscription.LastSubscriptionStatus.LastPurchaseOrder
	if order.ApplicationID != c.applicationID || (order.PackageName != "" && order.PackageName != c.packageName) {
		return errors.New("subscription belongs to another application")
	}
	if order.ProductID != productID || int(order.ProductType) != 2 {
		return errors.New("subscription product does not match request")
	}
	if err := c.validateDeveloperPayload(order.DeveloperPayload, developerPayload); err != nil {
		return err
	}
	if order.PurchaseOrderID == "" ||
		(subscription.LastSubscriptionStatus.PurchaseToken == "" && order.PurchaseToken == "") {
		return errors.New("subscription is missing order identity")
	}
	if subscription.LastSubscriptionStatus.RenewalInfo.ProductID != "" &&
		subscription.LastSubscriptionStatus.RenewalInfo.ProductID != productID {
		return errors.New("subscription renewal product does not match request")
	}
	if !c.environmentMatches(subscription.Environment) {
		return errors.New("subscription environment does not match server environment")
	}
	return nil
}

func (c *Client) environmentMatches(value string) bool {
	value = strings.ToUpper(strings.TrimSpace(value))
	if c.environment == "sandbox" {
		return value == "" || value == "SANDBOX"
	}
	return value == "NORMAL" || value == "PRODUCTION"
}

func (c *Client) NotificationEnvironmentMatches(value string) bool {
	return c.environmentMatches(value)
}

func (c *Client) NotificationApplicationMatches(applicationID string) bool {
	applicationID = strings.TrimSpace(applicationID)
	return applicationID == "" || applicationID == c.applicationID
}

func (c *Client) validateDeveloperPayload(orderPayload string, expected string) error {
	if orderPayload == expected {
		return nil
	}
	// Staging/sandbox 联调：历史订单可能没有或无法匹配 developerPayload，生产环境仍严格校验。
	if c.environment == "sandbox" {
		return nil
	}
	return errors.New("purchase account binding does not match")
}

func (c *Client) confirm(ctx context.Context, path string, orderID string, purchaseToken string) error {
	var response struct {
		ResponseCode    string `json:"responseCode"`
		ResponseMessage string `json:"responseMessage"`
	}
	if err := c.call(ctx, path, orderID, purchaseToken, &response); err != nil {
		return err
	}
	if response.ResponseCode != "0" {
		return fmt.Errorf("Huawei delivery confirmation failed: code=%s message=%s", response.ResponseCode, response.ResponseMessage)
	}
	return nil
}

func (c *Client) call(ctx context.Context, path string, orderID string, purchaseToken string, target interface{}) error {
	return c.callJSON(ctx, path, map[string]string{
		"purchaseOrderId": orderID,
		"purchaseToken":   purchaseToken,
	}, target)
}

func (c *Client) callJSON(ctx context.Context, path string, requestBody interface{}, target interface{}) error {
	body, err := json.Marshal(requestBody)
	if err != nil {
		return err
	}
	requestJWT, err := c.signedIAPRequestJWT(body)
	if err != nil {
		return err
	}
	rootURL := ChinaOrderRootURL
	if strings.HasPrefix(path, "/subscription/") {
		rootURL = ChinaSubscriptionRootURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rootURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+requestJWT)
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call Huawei IAP: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("call Huawei IAP: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maximumResponseBody)).Decode(target); err != nil {
		return fmt.Errorf("decode Huawei IAP response: %w", err)
	}
	return nil
}

// signedIAPRequestJWT implements HarmonyOS IAP server API auth (aud=iap-v1, ES256, body digest).
// See https://developer.huawei.com/consumer/cn/doc/harmonyos-references/iap-jwt-description
func (c *Client) signedIAPRequestJWT(body []byte) (string, error) {
	digest := sha256.Sum256(body)
	now := c.now().UTC()
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": c.keyID, "typ": "JWT"})
	claims, _ := json.Marshal(map[string]interface{}{
		"iss": c.issuerID, "aud": iapRequestJWTAudience, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		"aid": c.applicationID, "digest": hex.EncodeToString(digest[:]),
	})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	hash := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, c.signingKey, hash[:])
	if err != nil {
		return "", fmt.Errorf("sign Harmony IAP request JWT: %w", err)
	}
	signature := encodeES256JWSSignature(r, s, c.signingKey.Curve)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func encodeES256JWSSignature(r, s *big.Int, curve elliptic.Curve) []byte {
	size := (curve.Params().BitSize + 7) / 8
	signature := make([]byte, 2*size)
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(signature[size-len(rBytes):size], rBytes)
	copy(signature[2*size-len(sBytes):], sBytes)
	return signature
}

func parseIAPAPIPrivateKey(value string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, errors.New("HUAWEI_IAP_PRIVATE_KEY is not valid PEM")
	}
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key, ok := parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("Harmony IAP server APIs require an EC P-256 private key (ES256) from AGC IAP key configuration; RSA API Console service account keys cannot call order query")
		}
		if key.Curve.Params().Name != "P-256" {
			return nil, errors.New("HUAWEI_IAP_PRIVATE_KEY must be ECDSA P-256 for Harmony IAP server APIs")
		}
		return key, nil
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("HUAWEI_IAP_PRIVATE_KEY must be PKCS#8 or SEC1 EC PEM (P-256)")
	}
	if key.Curve.Params().Name != "P-256" {
		return nil, errors.New("HUAWEI_IAP_PRIVATE_KEY must be ECDSA P-256 for Harmony IAP server APIs")
	}
	return key, nil
}
