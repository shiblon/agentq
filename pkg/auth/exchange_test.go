package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExchange_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.FormValue("grant_type") != grantExchange {
			t.Errorf("grant_type = %q, want %q", r.FormValue("grant_type"), grantExchange)
		}
		if r.FormValue("subject_token") != "human-token" {
			t.Errorf("subject_token = %q, want %q", r.FormValue("subject_token"), "human-token")
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "client-id" || pass != "client-secret" {
			t.Errorf("basic auth = %q/%q ok=%v, want client-id/client-secret", user, pass, ok)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "agent-token-xyz",
			"expires_in":   3600,
		})
	}))
	defer srv.Close()

	e := NewTokenExchanger(srv.URL, "client-id", "client-secret")
	tok, err := e.Exchange(context.Background(), "human-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.AccessToken != "agent-token-xyz" {
		t.Errorf("access_token = %q, want %q", tok.AccessToken, "agent-token-xyz")
	}
	if tok.ExpiresAt.Before(time.Now().Add(59 * time.Minute)) {
		t.Error("expires_at should be ~1 hour from now")
	}
}

func TestExchange_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant","error_description":"token expired"}`))
	}))
	defer srv.Close()

	e := NewTokenExchanger(srv.URL, "client-id", "secret")
	_, err := e.Exchange(context.Background(), "bad-token")
	if err == nil {
		t.Error("expected error for non-200 response")
	}
}

func TestExchange_OAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"error":             "access_denied",
			"error_description": "impersonation not allowed",
		})
	}))
	defer srv.Close()

	e := NewTokenExchanger(srv.URL, "client-id", "secret")
	_, err := e.Exchange(context.Background(), "token")
	if err == nil {
		t.Error("expected error for oauth error response")
	}
}
