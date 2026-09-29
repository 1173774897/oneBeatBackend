package database

import (
	"context"
	"strings"
	"testing"
)

func TestOpenRequiresDatabaseURL(t *testing.T) {
	store, err := Open(context.Background(), "  ")
	if store != nil {
		store.Close()
		t.Fatal("Open returned a store without DATABASE_URL")
	}
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("error = %v, want DATABASE_URL validation error", err)
	}
}
