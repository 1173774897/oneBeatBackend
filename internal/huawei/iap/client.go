package iap

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	huaweijws "onebeat/store-api/internal/huawei/jws"
)

const (
	// The China site is fixed in code so production never guesses a data-processing site.
	ChinaOrderRootURL        = "https://orders-drcn.iap.cloud.huawei.com.cn"
	ChinaSubscriptionRootURL = "https://subscr-drcn.iap.cloud.huawei.com.cn"
	OAuthTokenURL            = "https://oauth-login.cloud.huawei.com/oauth2/v3/token"

	orderConfirmPath        = "/order/harmony/v1/application/purchase/shipped/confirm"
	orderStatusPath         = "/order/harmony/v1/application/order/status/query"
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
	privateKey    *rsa.PrivateKey
	keyID         string
	issuerID      string
	environment   string
	applicationID string
	packageName   string
	jwsVerifier   *huaweijws.Verifier
	now           func() time.Time

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
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
	privateKey, err := parsePrivateKey(config.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	verifier, err := huaweijws.NewVerifier()
	if err != nil {
		return nil, err
	}
	return &Client{
		httpClient: httpClient, privateKey: privateKey, keyID: config.KeyID, issuerID: config.IssuerID,
		environment: config.Environment, applicationID: config.ApplicationID, packageName: config.PackageName,
		jwsVerifier: verifier, now: time.Now,
	}, nil
}

func (c *Client) DecodePurchaseData(compact string) (PurchaseReference, error) {
	payload, err := c.jwsVerifier.Verify(compact)
	if err != nil {
		return PurchaseReference{}, fmt.Errorf("verify client purchase JWS: %w", err)
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
	payload, err := c.jwsVerifier.Verify(response.JWSPurchaseOrder)
	if err != nil {
		return PurchaseOrderPayload{}, fmt.Errorf("verify Huawei order response: %w", err)
	}
	var order PurchaseOrderPayload
	if err := json.Unmarshal(payload, &order); err != nil {
		return PurchaseOrderPayload{}, fmt.Errorf("decode Huawei order response: %w", err)
	}
	return order, nil
}

func (c *Client) QuerySubscription(ctx context.Context, orderID string, purchaseToken string) (SubGroupStatusPayload, error) {
	var response struct {
		ResponseCode      string `json:"responseCode"`
		ResponseMessage   string `json:"responseMessage"`
		JWSSubGroupStatus string `json:"jwsSubGroupStatus"`
	}
	if err := c.call(ctx, subscriptionStatusPath, orderID, purchaseToken, &response); err != nil {
		return SubGroupStatusPayload{}, err
	}
	if response.ResponseCode != "0" || response.JWSSubGroupStatus == "" {
		return SubGroupStatusPayload{}, fmt.Errorf("Huawei subscription query failed: code=%s message=%s", response.ResponseCode, response.ResponseMessage)
	}
	payload, err := c.jwsVerifier.Verify(response.JWSSubGroupStatus)
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
	if order.ProductID != productID || int(order.ProductType) != productType {
		return errors.New("purchase product does not match request")
	}
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
	value = strings.ToUpper(value)
	if c.environment == "sandbox" {
		return value == "SANDBOX"
	}
	return value == "NORMAL" || value == "PRODUCTION"
}

func (c *Client) validateDeveloperPayload(orderPayload string, expected string) error {
	if orderPayload == expected {
		return nil
	}
	// Sandbox orders created before client-side developerPayload wiring may return empty.
	if c.environment == "sandbox" && strings.TrimSpace(orderPayload) == "" {
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
	token, err := c.token(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"purchaseOrderId": orderID, "purchaseToken": purchaseToken})
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
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call Huawei IAP: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("call Huawei IAP: HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maximumResponseBody)).Decode(target); err != nil {
		return fmt.Errorf("decode Huawei IAP response: %w", err)
	}
	return nil
}

func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken != "" && c.now().Add(time.Minute).Before(c.tokenExpiry) {
		return c.accessToken, nil
	}
	assertion, err := c.signedAssertion()
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("exchange Huawei service assertion: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("exchange Huawei service assertion: HTTP %d", response.StatusCode)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maximumResponseBody)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode Huawei access token: %w", err)
	}
	if payload.AccessToken == "" {
		return "", errors.New("Huawei access token response was empty")
	}
	if payload.ExpiresIn <= 0 {
		payload.ExpiresIn = 3600
	}
	c.accessToken = payload.AccessToken
	c.tokenExpiry = c.now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	return c.accessToken, nil
}

func (c *Client) signedAssertion() (string, error) {
	now := c.now().UTC()
	header, _ := json.Marshal(map[string]string{"alg": "PS256", "kid": c.keyID, "typ": "JWT"})
	claims, _ := json.Marshal(map[string]interface{}{
		"iss": c.issuerID, "aud": OAuthTokenURL, "iat": now.Unix(), "exp": now.Add(55 * time.Minute).Unix(),
	})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	hash := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPSS(rand.Reader, c.privateKey, crypto.SHA256, hash[:], &rsa.PSSOptions{
		SaltLength: rsa.PSSSaltLengthEqualsHash,
		Hash:       crypto.SHA256,
	})
	if err != nil {
		return "", fmt.Errorf("sign Huawei service assertion: %w", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func parsePrivateKey(value string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, errors.New("HUAWEI_IAP_PRIVATE_KEY is not valid PEM")
	}
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("HUAWEI_IAP_PRIVATE_KEY is not an RSA key")
		}
		return key, nil
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("HUAWEI_IAP_PRIVATE_KEY must be PKCS#8 or PKCS#1 RSA PEM")
	}
	return key, nil
}
