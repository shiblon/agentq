package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	grantDeviceCode = "urn:ietf:params:oauth:grant-type:device_code"
	scopeOpenID     = "openid profile email"
)

// DeviceFlowClient initiates and polls an RFC 8628 device authorization flow.
type DeviceFlowClient struct {
	issuer   string
	clientID string
	httpClient *http.Client
}

// NewDeviceFlowClient creates a client for the given OIDC issuer and public
// client ID. No client secret is needed for device flow public clients.
func NewDeviceFlowClient(issuer, clientID string) *DeviceFlowClient {
	return &DeviceFlowClient{
		issuer:   strings.TrimRight(issuer, "/"),
		clientID: clientID,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// deviceAuthResponse is the response from the device authorization endpoint.
type deviceAuthResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	// Zitadel uses verification_uri_complete for the pre-filled URL.
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// tokenResponse is the polling response from the token endpoint.
type tokenPollResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

// Authorize starts device authorization and returns the codes the user needs.
// The caller should display UserCode and VerificationURI, then call Poll.
func (c *DeviceFlowClient) Authorize(ctx context.Context) (*deviceAuthResponse, error) {
	body := url.Values{
		"client_id": {c.clientID},
		"scope":     {scopeOpenID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.issuer+"/oauth/v2/device_authorization",
		strings.NewReader(body.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build device auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("device auth request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device auth failed (status %d): %s", resp.StatusCode, raw)
	}
	var dar deviceAuthResponse
	if err := json.Unmarshal(raw, &dar); err != nil {
		return nil, fmt.Errorf("parse device auth response: %w", err)
	}
	if dar.Interval == 0 {
		dar.Interval = 5
	}
	return &dar, nil
}

// Poll waits until the user completes authorization or the context is cancelled.
// Returns a Credentials ready to be saved.
func (c *DeviceFlowClient) Poll(ctx context.Context, dar *deviceAuthResponse) (*Credentials, error) {
	interval := time.Duration(dar.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(dar.ExpiresIn) * time.Second)

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("device authorization expired")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		body := url.Values{
			"grant_type":  {grantDeviceCode},
			"client_id":   {c.clientID},
			"device_code": {dar.DeviceCode},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.issuer+"/oauth/v2/token",
			strings.NewReader(body.Encode()))
		if err != nil {
			return nil, fmt.Errorf("build poll request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("poll request: %w", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var tr tokenPollResponse
		if err := json.Unmarshal(raw, &tr); err != nil {
			return nil, fmt.Errorf("parse poll response: %w", err)
		}

		switch tr.Error {
		case "":
			if tr.AccessToken == "" {
				return nil, fmt.Errorf("empty access token in response")
			}
			return &Credentials{
				AccessToken: tr.AccessToken,
				ExpiresAt:   time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
				Issuer:      c.issuer,
			}, nil
		case "authorization_pending", "slow_down":
			if tr.Error == "slow_down" {
				interval += 5 * time.Second
			}
			// Continue polling.
		default:
			return nil, fmt.Errorf("device flow error %q: %s", tr.Error, tr.ErrorDesc)
		}
	}
}
