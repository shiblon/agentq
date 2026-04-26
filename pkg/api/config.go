package api

import "net/http"

// appConfig is the shape returned by GET /api/v1/config.
// The frontend fetches this on startup to configure authentication.
type appConfig struct {
	Auth authConfig `json:"auth"`
}

type authConfig struct {
	Enabled  bool   `json:"enabled"`
	Issuer   string `json:"issuer,omitempty"`
	ClientID string `json:"client_id,omitempty"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg := appConfig{
		Auth: authConfig{
			Enabled:  s.validator != nil,
			Issuer:   s.issuer,
			ClientID: s.oidcClientID,
		},
	}
	writeJSON(w, http.StatusOK, cfg)
}
