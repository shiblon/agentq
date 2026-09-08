// Package auth provides token exchange and credential management for agentq.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

const (
	tokenTypeAccess = "urn:ietf:params:oauth:token-type:access_token"
	grantExchange   = "urn:ietf:params:oauth:grant-type:token-exchange"
)

// ExchangedToken is the result of a successful token exchange.
type ExchangedToken struct {
	AccessToken string
	ExpiresAt   time.Time
}

// clientCredentials holds the mutable client ID and secret.
type clientCredentials struct {
	clientID     string
	clientSecret string
}

// TokenExchanger exchanges a subject token (human's access token) for a
// delegated agent token using RFC 8693 token exchange against an OIDC
// provider. The resulting token carries act.sub = supervisor's subject,
// sub = human's subject.
//
// Credentials are stored atomically and can be reloaded at runtime by calling
// Reload -- for example in response to SIGHUP when using Vault Agent sidecar
// rotation.
type TokenExchanger struct {
	tokenURL         string
	clientIDFile     string // empty when credentials were provided directly
	clientSecretFile string
	creds            atomic.Pointer[clientCredentials]
	httpClient       *http.Client
}

// NewTokenExchanger creates an exchanger using credential values provided
// directly (e.g. from environment variables or flags). Reload is a no-op.
func NewTokenExchanger(tokenURL, clientID, clientSecret string) *TokenExchanger {
	e := &TokenExchanger{
		tokenURL:   tokenURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
	e.creds.Store(&clientCredentials{clientID: clientID, clientSecret: clientSecret})
	return e
}

// NewTokenExchangerFromFiles creates an exchanger that reads credentials from
// files. Use this with Vault Agent or any sidecar that writes rotated
// credentials to disk. Call Reload (e.g. on SIGHUP) to pick up new values.
func NewTokenExchangerFromFiles(tokenURL, clientIDFile, clientSecretFile string) (*TokenExchanger, error) {
	e := &TokenExchanger{
		tokenURL:         tokenURL,
		clientIDFile:     clientIDFile,
		clientSecretFile: clientSecretFile,
		httpClient:       &http.Client{Timeout: 10 * time.Second},
	}
	if err := e.Reload(); err != nil {
		return nil, fmt.Errorf("initial credential load: %w", err)
	}
	return e, nil
}

// Reload re-reads credentials from disk. No-op when credentials were provided
// directly via NewTokenExchanger. Safe to call from a signal handler.
func (e *TokenExchanger) Reload() error {
	if e.clientIDFile == "" {
		return nil
	}
	id, err := readTrimmed(e.clientIDFile)
	if err != nil {
		return fmt.Errorf("read client ID file %q: %w", e.clientIDFile, err)
	}
	secret, err := readTrimmed(e.clientSecretFile)
	if err != nil {
		return fmt.Errorf("read client secret file %q: %w", e.clientSecretFile, err)
	}
	e.creds.Store(&clientCredentials{clientID: id, clientSecret: secret})
	return nil
}

// Exchange performs the RFC 8693 token exchange. subjectToken is the human's
// current access token. The returned ExchangedToken can be passed to agents
// as their bearer credential.
func (e *TokenExchanger) Exchange(ctx context.Context, subjectToken string) (*ExchangedToken, error) {
	creds := e.creds.Load()

	body := url.Values{
		"grant_type":           {grantExchange},
		"subject_token":        {subjectToken},
		"subject_token_type":   {tokenTypeAccess},
		"requested_token_type": {tokenTypeAccess},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.tokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(creds.clientID, creds.clientSecret)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read exchange response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, raw)
	}

	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse exchange response: %w", err)
	}
	if result.Error != "" {
		return nil, fmt.Errorf("token exchange error %q: %s", result.Error, result.ErrorDesc)
	}
	if result.AccessToken == "" {
		return nil, fmt.Errorf("token exchange returned empty access token")
	}

	expiresAt := time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	return &ExchangedToken{AccessToken: result.AccessToken, ExpiresAt: expiresAt}, nil
}

func readTrimmed(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
