package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type fakeIDTokenVerifier struct {
	identities map[string]HuaweiIdentity
}

func (f fakeIDTokenVerifier) Verify(_ context.Context, token string) (HuaweiIdentity, error) {
	return f.identities[token], nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestHuaweiAccountAuthenticatorObtainsTrustedUnionID(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"access_token":"account-access","id_token":"server-id-token"}`
		if request.URL.Path == "/userinfo" {
			body = `{"openID":"subject-123","unionID":"UnionID-CaseSensitive"}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	authenticator := NewHuaweiAccountAuthenticator(client, "client-id", "client-secret", fakeIDTokenVerifier{
		identities: map[string]HuaweiIdentity{
			"client-id-token": {Subject: "subject-123"},
			"server-id-token": {Subject: "subject-123"},
		},
	})
	authenticator.tokenURL = "https://example.test/token"
	authenticator.userInfoURL = "https://example.test/userinfo"

	identity, err := authenticator.Authenticate(context.Background(), "client-id-token", "one-use-code")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if identity.UnionID != "UnionID-CaseSensitive" || identity.Subject != "subject-123" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
}

func TestHuaweiAccountAuthenticatorRejectsMismatchedAuthorizationCode(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"account-access","id_token":"server-id-token"}`)),
		}, nil
	})}
	authenticator := NewHuaweiAccountAuthenticator(client, "client-id", "client-secret", fakeIDTokenVerifier{
		identities: map[string]HuaweiIdentity{
			"client-id-token": {Subject: "subject-a"},
			"server-id-token": {Subject: "subject-b"},
		},
	})
	authenticator.tokenURL = "https://example.test/token"

	if _, err := authenticator.Authenticate(context.Background(), "client-id-token", "wrong-account-code"); err == nil {
		t.Fatal("mismatched authorization code unexpectedly authenticated")
	}
}

func TestExchangeAuthorizationCodeOmitsRedirectURIAndKeepsHuaweiErrorCodes(t *testing.T) {
	var form url.Values
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		parsed, err := url.ParseQuery(string(raw))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}
		form = parsed
		body := `{"error":1203,"sub_error":12304,"error_description":"invalid client_secret","access_token":"secret-access-token","id_token":"header.payload.signature"}`
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	authenticator := NewHuaweiAccountAuthenticator(client, "client-id", "client-secret", fakeIDTokenVerifier{
		identities: map[string]HuaweiIdentity{"client-id-token": {Subject: "subject-123"}},
	})
	authenticator.tokenURL = "https://example.test/token"

	_, err := authenticator.Authenticate(context.Background(), "client-id-token", "one-use-code")
	if form.Get("redirect_uri") != "" {
		t.Fatalf("redirect_uri = %q", form.Get("redirect_uri"))
	}
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "one-use-code" || form.Get("client_id") != "client-id" {
		t.Fatalf("unexpected token request: %v", form)
	}
	var apiError *HuaweiAPIError
	if !errors.As(err, &apiError) {
		t.Fatalf("error = %v", err)
	}
	if apiError.HTTPStatus != http.StatusBadRequest || apiError.ErrorCode != "1203" || apiError.SubError != "12304" {
		t.Fatalf("unexpected Huawei error: %+v", apiError)
	}
	if strings.Contains(err.Error(), "secret-access-token") || strings.Contains(err.Error(), "header.payload") || strings.Contains(err.Error(), "client-secret") {
		t.Fatalf("error leaked credential material: %v", err)
	}
}
