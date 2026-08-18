package handlers

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/shanehull/obsidian-remote/internal/config"
)

// OAuthCallbackPath is where the upstream provider sends the user back. We keep
// the provider's redirect on our own origin so the authorization response we
// hand to the client carries our issuer (RFC 9207) instead of the provider's.
const OAuthCallbackPath = "/oauth/callback"

// fallbackStateKey signs the state envelope when no client secret is configured.
// A per-process key is enough: it only has to outlive a single login.
var fallbackStateKey = func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		slog.Error("failed to generate state signing key", "error", err)
	}
	return key
}()

// requestHost resolves the public origin of the server as seen by the client,
// honouring the reverse proxy's forwarded scheme.
func requestHost(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s", scheme, r.Host)
}

func callbackURL(r *http.Request) string {
	return strings.TrimSuffix(requestHost(r), "/") + OAuthCallbackPath
}

// authState is what we stash in the upstream `state` so the callback can find
// its way back to the client that started the flow.
type authState struct {
	ClientState string `json:"s"`
	RedirectURI string `json:"r"`
}

func stateKey(cfg *config.Config) []byte {
	if cfg.OAuthClientSecret != "" {
		return []byte(cfg.OAuthClientSecret)
	}
	return fallbackStateKey
}

func encodeState(cfg *config.Config, st authState) (string, error) {
	payload, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, stateKey(cfg))
	mac.Write(payload)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

func decodeState(cfg *config.Config, raw string) (authState, error) {
	var st authState

	body, sig, ok := strings.Cut(raw, ".")
	if !ok {
		return st, fmt.Errorf("malformed state")
	}

	enc := base64.RawURLEncoding
	payload, err := enc.DecodeString(body)
	if err != nil {
		return st, fmt.Errorf("undecodable state: %w", err)
	}
	gotMAC, err := enc.DecodeString(sig)
	if err != nil {
		return st, fmt.Errorf("undecodable state signature: %w", err)
	}

	mac := hmac.New(sha256.New, stateKey(cfg))
	mac.Write(payload)
	if subtle.ConstantTimeCompare(gotMAC, mac.Sum(nil)) != 1 {
		return st, fmt.Errorf("state signature mismatch")
	}

	if err := json.Unmarshal(payload, &st); err != nil {
		return st, fmt.Errorf("unparseable state: %w", err)
	}
	if !validRedirectURI(st.RedirectURI) {
		return st, fmt.Errorf("invalid redirect_uri in state")
	}
	return st, nil
}

// validRedirectURI keeps the callback from becoming a general purpose redirector.
func validRedirectURI(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// HandleAuthorizeProxy starts the upstream flow on our own callback, so the
// provider never redirects straight to the client. It also injects the default
// scope so clients don't have to know about it.
func HandleAuthorizeProxy(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params := r.URL.Query()
		if params.Get("scope") == "" {
			params.Set("scope", "openid email profile")
		}

		// Ask for offline access so the provider issues a refresh token, and for a
		// fresh consent because Google only hands the refresh token out when consent
		// is granted again. Without both, our metadata advertises a refresh_token
		// grant the flow can never satisfy and the client re-authenticates by hand
		// every time the access token expires. A client that sets these itself wins.
		if params.Get("access_type") == "" {
			params.Set("access_type", "offline")
		}
		if params.Get("prompt") == "" {
			params.Set("prompt", "consent")
		}

		clientRedirect := params.Get("redirect_uri")
		if validRedirectURI(clientRedirect) {
			state, err := encodeState(cfg, authState{
				ClientState: params.Get("state"),
				RedirectURI: clientRedirect,
			})
			if err != nil {
				slog.Error("authorize: failed to encode state", "error", err)
				http.Error(w, "Authorization failed", http.StatusInternalServerError)
				return
			}
			params.Set("redirect_uri", callbackURL(r))
			params.Set("state", state)
		} else if clientRedirect != "" {
			slog.Warn("authorize: rejecting unusable redirect_uri", "redirect_uri", clientRedirect)
			http.Error(w, "Invalid redirect_uri", http.StatusBadRequest)
			return
		}

		// `resource` is for us, not for the upstream provider.
		params.Del("resource")

		http.Redirect(w, r, cfg.OAuthAuthorizeURL+"?"+params.Encode(), http.StatusFound)
	}
}

// HandleOAuthCallback relays the provider's authorization response to the
// client that started the flow, restoring its original state and stamping our
// own issuer so RFC 9207 validation passes.
func HandleOAuthCallback(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params := r.URL.Query()

		st, err := decodeState(cfg, params.Get("state"))
		if err != nil {
			slog.Warn("callback: unusable state", "error", err, "remote", r.RemoteAddr)
			http.Error(w, "Invalid state", http.StatusBadRequest)
			return
		}

		target, err := url.Parse(st.RedirectURI)
		if err != nil {
			http.Error(w, "Invalid state", http.StatusBadRequest)
			return
		}

		out := target.Query()
		for _, k := range []string{"code", "error", "error_description", "error_uri", "scope"} {
			if v := params.Get(k); v != "" {
				out.Set(k, v)
			}
		}
		if st.ClientState != "" {
			out.Set("state", st.ClientState)
		}
		out.Set("iss", strings.TrimSuffix(requestHost(r), "/"))
		target.RawQuery = out.Encode()

		http.Redirect(w, r, target.String(), http.StatusFound)
	}
}

// HandleTokenProxy forwards the code exchange upstream, injecting the client
// credentials and the redirect_uri the provider actually saw (ours, not the
// client's) so the exchange matches the authorization request.
func HandleTokenProxy(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		form := url.Values{}
		for k, v := range r.Form {
			form[k] = v
		}
		form.Set("client_id", cfg.OAuthAudience)
		if cfg.OAuthClientSecret != "" {
			form.Set("client_secret", cfg.OAuthClientSecret)
		}
		if form.Get("grant_type") == "authorization_code" {
			form.Set("redirect_uri", callbackURL(r))
		}
		form.Del("resource")

		resp, err := http.PostForm(cfg.OAuthTokenURL, form)
		if err != nil {
			slog.Error("token proxy: upstream request failed", "error", err)
			http.Error(w, "Token exchange failed", http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()

		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(w, resp.Body); err != nil {
			slog.Error("token proxy: failed to write response", "error", err)
		}
	}
}
