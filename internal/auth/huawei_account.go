package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	huaweiAccountTokenURL    = "https://oauth-login.cloud.huawei.com/oauth2/v3/token"
	huaweiAccountUserInfoURL = "https://account.cloud.huawei.com/rest.php?nsp_svc=GOpen.User.getInfo"
	maximumAccountBody       = 1 << 20
)

// HuaweiAPIError is a Huawei Account response that failed. It carries only HTTP status and
// Huawei error codes, never tokens, authorization codes, or client secrets.
type HuaweiAPIError struct {
	Operation  string
	HTTPStatus int
	ErrorCode  string
	SubError   string
}

func (e *HuaweiAPIError) Error() string {
	return fmt.Sprintf("%s: HTTP %d error=%s sub_error=%s", e.Operation, e.HTTPStatus, e.ErrorCode, e.SubError)
}

type idTokenVerifier interface {
	Verify(context.Context, string) (HuaweiIdentity, error)
}

// HuaweiAccountAuthenticator binds the client result to a one-use authorization code and obtains
// the account's UnionID from Huawei instead of trusting an identifier supplied by the client.
type HuaweiAccountAuthenticator struct {
	client       *http.Client
	clientID     string
	clientSecret string
	verifier     idTokenVerifier
	tokenURL     string
	userInfoURL  string
}

func NewHuaweiAccountAuthenticator(
	client *http.Client,
	clientID string,
	clientSecret string,
	verifier idTokenVerifier,
) *HuaweiAccountAuthenticator {
	return &HuaweiAccountAuthenticator{
		client: client, clientID: clientID, clientSecret: clientSecret, verifier: verifier,
		tokenURL: huaweiAccountTokenURL, userInfoURL: huaweiAccountUserInfoURL,
	}
}

func (a *HuaweiAccountAuthenticator) Authenticate(
	ctx context.Context,
	clientIDToken string,
	authorizationCode string,
) (HuaweiIdentity, error) {
	clientIdentity, err := a.verifier.Verify(ctx, clientIDToken)
	if err != nil {
		return HuaweiIdentity{}, fmt.Errorf("verify client ID token: %w", err)
	}
	credentials, err := a.exchangeAuthorizationCode(ctx, authorizationCode)
	if err != nil {
		return HuaweiIdentity{}, err
	}
	serverIdentity, err := a.verifier.Verify(ctx, credentials.IDToken)
	if err != nil {
		return HuaweiIdentity{}, fmt.Errorf("verify exchanged ID token: %w", err)
	}
	if clientIdentity.Subject != serverIdentity.Subject {
		return HuaweiIdentity{}, errors.New("authorization code belongs to another Huawei account")
	}

	accountInfo, err := a.fetchUserInfo(ctx, credentials.AccessToken)
	if err != nil {
		return HuaweiIdentity{}, err
	}
	unionID := firstNonEmpty(accountInfo.UnionID, serverIdentity.UnionID)
	if unionID == "" {
		return HuaweiIdentity{}, errors.New("Huawei account response did not contain UnionID")
	}
	if serverIdentity.UnionID != "" && serverIdentity.UnionID != unionID {
		return HuaweiIdentity{}, errors.New("Huawei UnionID differs between verified responses")
	}
	return HuaweiIdentity{UnionID: unionID, Subject: serverIdentity.Subject}, nil
}

type accountCredentials struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
}

func (a *HuaweiAccountAuthenticator) exchangeAuthorizationCode(
	ctx context.Context,
	authorizationCode string,
) (accountCredentials, error) {
	if strings.TrimSpace(authorizationCode) == "" {
		return accountCredentials{}, errors.New("Huawei authorization code is empty")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", authorizationCode)
	form.Set("client_id", a.clientID)
	form.Set("client_secret", a.clientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return accountCredentials{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := a.client.Do(req)
	if err != nil {
		return accountCredentials{}, fmt.Errorf("exchange Huawei authorization code: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return accountCredentials{}, readHuaweiAPIError("exchange Huawei authorization code", response.StatusCode, response.Body)
	}
	var credentials accountCredentials
	if err := json.NewDecoder(io.LimitReader(response.Body, maximumAccountBody)).Decode(&credentials); err != nil {
		return accountCredentials{}, fmt.Errorf("decode Huawei account credentials: %w", err)
	}
	if credentials.AccessToken == "" || credentials.IDToken == "" {
		return accountCredentials{}, errors.New("Huawei account credential response was incomplete")
	}
	return credentials, nil
}

type huaweiAccountInfo struct {
	OpenID  string
	UnionID string
}

func (info *huaweiAccountInfo) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	info.OpenID = firstNonEmpty(
		jsonStringField(raw, "openID"),
		jsonStringField(raw, "openId"),
		jsonStringField(raw, "openid"),
	)
	info.UnionID = firstNonEmpty(
		jsonStringField(raw, "unionID"),
		jsonStringField(raw, "unionId"),
		jsonStringField(raw, "union_id"),
	)
	return nil
}

func jsonStringField(raw map[string]json.RawMessage, key string) string {
	value, ok := raw[key]
	if !ok {
		return ""
	}
	var text string
	if json.Unmarshal(value, &text) != nil {
		return ""
	}
	return strings.TrimSpace(text)
}

func (a *HuaweiAccountAuthenticator) fetchUserInfo(
	ctx context.Context,
	accessToken string,
) (huaweiAccountInfo, error) {
	form := url.Values{}
	form.Set("access_token", accessToken)
	form.Set("getNickName", "0")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.userInfoURL, strings.NewReader(form.Encode()))
	if err != nil {
		return huaweiAccountInfo{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := a.client.Do(req)
	if err != nil {
		return huaweiAccountInfo{}, fmt.Errorf("fetch Huawei user information: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return huaweiAccountInfo{}, readHuaweiAPIError("fetch Huawei user information", response.StatusCode, response.Body)
	}
	var accountInfo huaweiAccountInfo
	if err := json.NewDecoder(io.LimitReader(response.Body, maximumAccountBody)).Decode(&accountInfo); err != nil {
		return huaweiAccountInfo{}, fmt.Errorf("decode Huawei user information: %w", err)
	}
	return accountInfo, nil
}

func readHuaweiAPIError(operation string, statusCode int, body io.Reader) error {
	payload, err := io.ReadAll(io.LimitReader(body, maximumAccountBody))
	apiError := &HuaweiAPIError{Operation: operation, HTTPStatus: statusCode}
	if err != nil {
		return apiError
	}
	var parsed struct {
		Error    json.RawMessage `json:"error"`
		SubError json.RawMessage `json:"sub_error"`
	}
	if json.Unmarshal(payload, &parsed) == nil {
		apiError.ErrorCode = huaweiErrorCode(parsed.Error)
		apiError.SubError = huaweiErrorCode(parsed.SubError)
	}
	return apiError
}

func huaweiErrorCode(raw json.RawMessage) string {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] != '"' {
		for _, char := range raw {
			if char < '0' || char > '9' {
				return ""
			}
		}
		if len(raw) > 10 {
			return ""
		}
		return string(raw)
	}
	var text string
	if json.Unmarshal(raw, &text) != nil || !safeHuaweiErrorCode(text) {
		return ""
	}
	return text
}

func safeHuaweiErrorCode(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '_' || char == '-':
		default:
			return false
		}
	}
	return true
}
