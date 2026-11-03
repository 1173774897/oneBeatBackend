package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"onebeat/store-api/internal/auth"
	"onebeat/store-api/internal/store/repository"
	storeservice "onebeat/store-api/internal/store/service"
)

const testUserID = "6f1c2d3e-4a5b-4c6d-8e7f-901234567890"

type statusUserRepository struct {
	status string
	err    error
}

func (statusUserRepository) UpsertUser(context.Context, []byte, time.Time) (string, error) {
	return "", errors.New("not used")
}

func (s statusUserRepository) UserStatus(context.Context, string) (string, error) {
	return s.status, s.err
}

type stubAccountDeleter struct {
	deletedAt time.Time
	err       error
	calls     *int
}

func (s stubAccountDeleter) DeleteAccount(_ context.Context, userID string) (time.Time, error) {
	if s.calls != nil {
		*s.calls++
	}
	if userID != testUserID {
		return time.Time{}, errors.New("unexpected user")
	}
	return s.deletedAt, s.err
}

func deletionHandler(t *testing.T, status string, deleter stubAccountDeleter) (http.Handler, string) {
	t.Helper()
	sessions := auth.NewSessionManager([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	token, _, err := sessions.Issue(testUserID)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithStoreServices(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", "test-version",
		&fakeDatabase{}, &StoreServices{
			UserRepository: statusUserRepository{status: status},
			SessionManager: sessions,
			// Only non-nil-ness is checked before the account status gate.
			StoreService:   storeservice.New(nil, "test"),
			AccountDeleter: deleter,
		})
	return handler, token
}

func call(handler http.Handler, method string, path string, token string) (int, decodedEnvelope) {
	request := httptest.NewRequest(method, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var envelope decodedEnvelope
	_ = json.Unmarshal(recorder.Body.Bytes(), &envelope)
	return recorder.Code, envelope
}

func TestAccountDeletionContract(t *testing.T) {
	deletedAt := time.Date(2026, time.November, 3, 8, 0, 0, 0, time.UTC)
	calls := 0
	handler, token := deletionHandler(t, repository.UserStatusActive,
		stubAccountDeleter{deletedAt: deletedAt, calls: &calls})

	status, envelope := call(handler, http.MethodPost, "/api/v1/account/deletion", token)
	if status != http.StatusOK || envelope.Code != 0 || calls != 1 {
		t.Fatalf("delete = %d %+v calls=%d", status, envelope, calls)
	}
	var data struct {
		DeletedAt time.Time `json:"deletedAt"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil || !data.DeletedAt.Equal(deletedAt) {
		t.Fatalf("deletedAt = %s (%v)", envelope.Data, err)
	}

	if status, envelope = call(handler, http.MethodPost, "/api/v1/account/deletion", ""); status != http.StatusUnauthorized ||
		envelope.Code != 40101 {
		t.Fatalf("no session = %d %+v", status, envelope)
	}
	if status, _ = call(handler, http.MethodGet, "/api/v1/account/deletion", token); status != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", status)
	}
}

func TestAccountDeletionRetryWithDeletedSession(t *testing.T) {
	// A client that lost the first response retries with the same, now deleted,
	// session; the deleter itself is idempotent and returns the original time.
	deletedAt := time.Date(2026, time.November, 3, 8, 0, 0, 0, time.UTC)
	handler, token := deletionHandler(t, repository.UserStatusDeleted, stubAccountDeleter{deletedAt: deletedAt})
	if status, envelope := call(handler, http.MethodPost, "/api/v1/account/deletion", token); status != http.StatusOK ||
		envelope.Code != 0 {
		t.Fatalf("retry = %d %+v", status, envelope)
	}
}

func TestAccountDeletionErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   int
	}{
		{repository.ErrUserNotFound, http.StatusUnauthorized, 40101},
		{errors.New("database down"), http.StatusServiceUnavailable, 50301},
	}
	for _, tc := range cases {
		handler, token := deletionHandler(t, repository.UserStatusActive, stubAccountDeleter{err: tc.err})
		status, envelope := call(handler, http.MethodPost, "/api/v1/account/deletion", token)
		if status != tc.status || envelope.Code != tc.code || len(envelope.Data) != 0 {
			t.Fatalf("%v: got %d %+v", tc.err, status, envelope)
		}
	}
}

func TestDeletedAccountSessionIsRejectedElsewhere(t *testing.T) {
	// Stateless JWTs stay valid until expiry; the status gate revokes them.
	handler, token := deletionHandler(t, repository.UserStatusDeleted, stubAccountDeleter{})
	for _, path := range []string{"/api/v1/store/redemptions"} {
		if status, envelope := call(handler, http.MethodPost, path, token); status != http.StatusUnauthorized ||
			envelope.Code != 40101 {
			t.Fatalf("%s = %d %+v", path, status, envelope)
		}
	}
	if status, envelope := call(handler, http.MethodGet, "/api/v1/store/bootstrap", token); status != http.StatusUnauthorized ||
		envelope.Code != 40101 {
		t.Fatalf("bootstrap = %d %+v", status, envelope)
	}
	disabled, disabledToken := deletionHandler(t, "DISABLED", stubAccountDeleter{})
	if status, _ := call(disabled, http.MethodPost, "/api/v1/store/redemptions", disabledToken); status != http.StatusUnauthorized {
		t.Fatalf("disabled redemption = %d", status)
	}
}

// Purchase data must never reach this server (cross-border, 2026-11-03).
func TestNoPurchaseRoutesRemain(t *testing.T) {
	handler, token := deletionHandler(t, repository.UserStatusActive, stubAccountDeleter{})
	for _, path := range []string{"/api/v1/iap/purchases/verify", "/api/v1/iap/purchases/restore", "/api/v1/webhooks/huawei/iap"} {
		if status, _ := call(handler, http.MethodPost, path, token); status != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", path, status)
		}
	}
}
