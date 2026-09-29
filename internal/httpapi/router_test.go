package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type decodedEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func TestTestEndpointOverHTTPS(t *testing.T) {
	server := httptest.NewTLSServer(newTestHandler())
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/api/v1/test")
	if err != nil {
		t.Fatalf("GET test endpoint: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := response.Header.Get("Strict-Transport-Security"); got == "" {
		t.Fatal("Strict-Transport-Security header is missing")
	}

	var envelope decodedEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Code != 0 || envelope.Message != "ok" {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}

	var data testResponse
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if data.Service != "onebeat-store-api" || data.Status != "ready" || data.Scheme != "https" ||
		data.Environment != "test" || data.Version != "test-version" {
		t.Fatalf("unexpected data: %+v", data)
	}
}

func TestTestEndpointRejectsPost(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/test", nil)
	recorder := httptest.NewRecorder()

	newTestHandler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
	if got := recorder.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q, want %q", got, http.MethodGet)
	}
}

func TestUnknownEndpointReturnsJSON(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/missing", nil)
	recorder := httptest.NewRecorder()

	newTestHandler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func newTestHandler() http.Handler {
	return NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", "test-version")
}
