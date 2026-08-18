package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/shanehull/obsidian-remote/internal/config"
)

func HandleDiscovery(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		host := requestHost(r)

		resourcePath := strings.TrimPrefix(r.URL.Path, "/.well-known/oauth-protected-resource")
		if resourcePath == "" {
			resourcePath = "/mcp"
		}

		slog.Info("discovery request", "host", host, "path", r.URL.Path, "resource_path", resourcePath)

		res := map[string]interface{}{
			"resource":                 fmt.Sprintf("%s%s", strings.TrimSuffix(host, "/"), resourcePath),
			"resource_name":            "obsidian-remote",
			"authorization_servers":    []string{host}, // Tell the CLI to look at us for the login URLs
			"client_id":                cfg.OAuthAudience,
			"clientId":                 cfg.OAuthAudience,
			"bearer_methods_supported": []string{"header"},
		}

		if err := json.NewEncoder(w).Encode(res); err != nil {
			slog.Error("failed to encode discovery response", "error", err)
		}
	}
}

func HandleAuthServerDiscovery(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		host := requestHost(r)

		// This is the RFC 8414 / OpenID Configuration the CLI is looking for
		res := map[string]interface{}{
			"issuer":                           host,
			"authorization_endpoint":           fmt.Sprintf("%s/authorize", host),
			"token_endpoint":                   fmt.Sprintf("%s/token", host),
			"registration_endpoint":            fmt.Sprintf("%s/register", host), // Add this!
			"jwks_uri":                         cfg.OAuthJwksURL,
			"response_types_supported":         []string{"code"},
			"grant_types_supported":            []string{"authorization_code", "refresh_token"},
			"scopes_supported":                 []string{"openid", "email", "profile"},
			"code_challenge_methods_supported": []string{"S256"},
			"authorization_response_iss_parameter_supported": true,
		}

		if err := json.NewEncoder(w).Encode(res); err != nil {
			slog.Error("failed to encode auth server discovery response", "error", err)
		}
	}
}

func HandleRegistration(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var req map[string]interface{}
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				slog.Warn("registration: failed to decode body", "error", err)
			}
		}

		redirectURIs, _ := req["redirect_uris"].([]interface{})
		if redirectURIs == nil {
			redirectURIs = []interface{}{"http://localhost"}
		}

		scope, _ := req["scope"].(string)
		if scope == "" {
			scope = "openid email profile"
		}

		res := map[string]interface{}{
			"client_id":                  cfg.OAuthAudience,
			"client_id_issued_at":        0,
			"client_secret_expires_at":   0,
			"redirect_uris":              redirectURIs,
			"scope":                      scope,
			"token_endpoint_auth_method": "none",
		}

		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(res); err != nil {
			slog.Error("failed to encode registration response", "error", err)
		}
	}
}

func HandleConfig(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		res := map[string]string{
			"type":     "oauth",
			"issuer":   cfg.OAuthIssuer,
			"clientId": cfg.OAuthAudience,
		}

		if err := json.NewEncoder(w).Encode(res); err != nil {
			slog.Error("failed to encode config response", "error", err)
		}
	}
}

func HandleHealthz(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		host := cfg.ObsidianURL
		host = strings.TrimPrefix(host, "http://")
		host = strings.TrimPrefix(host, "https://")

		conn, err := net.DialTimeout("tcp", host, 500*time.Millisecond)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		_ = conn.Close()

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}
