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
	huaweiAccountRedirectURI = "hms://redirect_url"
	maximumAccountBody       = 1 << 20
)

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
	if accountInfo.OpenID != "" && accountInfo.OpenID != serverIdentity.Subject {
		return HuaweiIdentity{}, errors.New("Huawei user information does not match the ID token")
	}
	if accountInfo.UnionID == "" {
		accountInfo.UnionID = serverIdentity.UnionID
	}
	if accountInfo.UnionID == "" {
		return HuaweiIdentity{}, errors.New("Huawei account response did not contain UnionID")
	}
	if serverIdentity.UnionID != "" && serverIdentity.UnionID != accountInfo.UnionID {
		return HuaweiIdentity{}, errors.New("Huawei UnionID differs between verified responses")
	}
	return HuaweiIdentity{UnionID: accountInfo.UnionID, Subject: serverIdentity.Subject}, nil
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
	form.Set("redirect_uri", huaweiAccountRedirectURI)
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
		return accountCredentials{}, fmt.Errorf("exchange Huawei authorization code: HTTP %d", response.StatusCode)
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
	OpenID  string `json:"openID"`
	UnionID string `json:"unionID"`
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
		return huaweiAccountInfo{}, fmt.Errorf("fetch Huawei user information: HTTP %d", response.StatusCode)
	}
	var accountInfo huaweiAccountInfo
	if err := json.NewDecoder(io.LimitReader(response.Body, maximumAccountBody)).Decode(&accountInfo); err != nil {
		return huaweiAccountInfo{}, fmt.Errorf("decode Huawei user information: %w", err)
	}
	return accountInfo, nil
}
