package auth

import (
	"context"
	"io"
	"net/http"
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
