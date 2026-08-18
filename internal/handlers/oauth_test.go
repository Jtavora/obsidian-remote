package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/shanehull/obsidian-remote/internal/config"
	"github.com/shanehull/obsidian-remote/internal/handlers"
)

func oauthConfig() *config.Config {
	cfg := testConfig()
	cfg.OAuthClientSecret = "test-secret"
	return cfg
}

// authorizeState runs the authorize proxy and returns the state it handed upstream.
func authorizeState(t *testing.T, cfg *config.Config, clientRedirect, clientState string) string {
	t.Helper()

	q := url.Values{"client_id": {"x"}, "redirect_uri": {clientRedirect}, "state": {clientState}}
	req := httptest.NewRequest("GET", "/authorize?"+q.Encode(), nil)
	req.Host = "vault.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	handlers.HandleAuthorizeProxy(cfg).ServeHTTP(rec, req)

	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("unparseable Location: %v", err)
	}
	got := loc.Query().Get("redirect_uri")
	want := "https://vault.example.com" + handlers.OAuthCallbackPath
	if got != want {
		t.Fatalf("upstream redirect_uri = %s, want %s", got, want)
	}
	state := loc.Query().Get("state")
	if state == "" || state == clientState {
		t.Fatalf("expected wrapped state, got %q", state)
	}
	return state
}

func TestHandleAuthorizeProxy_RewritesRedirectAndState(t *testing.T) {
	authorizeState(t, oauthConfig(), "http://localhost:51000/callback", "client-state")
}

func TestHandleAuthorizeProxy_RejectsBadRedirect(t *testing.T) {
	req := httptest.NewRequest("GET", "/authorize?client_id=x&redirect_uri="+url.QueryEscape("javascript:alert(1)"), nil)
	rec := httptest.NewRecorder()
	handlers.HandleAuthorizeProxy(oauthConfig()).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleOAuthCallback_RestoresStateAndStampsIssuer(t *testing.T) {
	cfg := oauthConfig()
	state := authorizeState(t, cfg, "http://localhost:51000/callback", "client-state")

	req := httptest.NewRequest("GET", handlers.OAuthCallbackPath+"?code=abc&state="+url.QueryEscape(state), nil)
	req.Host = "vault.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	handlers.HandleOAuthCallback(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("unparseable Location: %v", err)
	}
	if !strings.HasPrefix(loc.String(), "http://localhost:51000/callback?") {
		t.Errorf("expected redirect back to the client, got %s", loc)
	}
	if got := loc.Query().Get("iss"); got != "https://vault.example.com" {
		t.Errorf("iss = %q, want our own issuer", got)
	}
	if got := loc.Query().Get("state"); got != "client-state" {
		t.Errorf("state = %q, want the client's original state", got)
	}
	if got := loc.Query().Get("code"); got != "abc" {
		t.Errorf("code = %q, want abc", got)
	}
}

func TestHandleOAuthCallback_ForwardsUpstreamError(t *testing.T) {
	cfg := oauthConfig()
	state := authorizeState(t, cfg, "http://localhost:51000/callback", "client-state")

	req := httptest.NewRequest("GET", handlers.OAuthCallbackPath+"?error=access_denied&state="+url.QueryEscape(state), nil)
	rec := httptest.NewRecorder()
	handlers.HandleOAuthCallback(cfg).ServeHTTP(rec, req)

	loc, _ := url.Parse(rec.Header().Get("Location"))
	if got := loc.Query().Get("error"); got != "access_denied" {
		t.Errorf("error = %q, want access_denied", got)
	}
}

func TestHandleOAuthCallback_RejectsForgedState(t *testing.T) {
	for _, state := range []string{"", "garbage", "eyJyIjoiaHR0cDovL2V2aWwuY29tIn0.YWJj"} {
		req := httptest.NewRequest("GET", handlers.OAuthCallbackPath+"?code=abc&state="+url.QueryEscape(state), nil)
		rec := httptest.NewRecorder()
		handlers.HandleOAuthCallback(oauthConfig()).ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("state %q: expected 400, got %d (Location: %s)", state, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestHandleTokenProxy_OverridesRedirectURI(t *testing.T) {
	var got url.Values
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.Form
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"t"}`))
	}))
	defer upstream.Close()

	cfg := oauthConfig()
	cfg.OAuthTokenURL = upstream.URL

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"abc"},
		"redirect_uri":  {"http://localhost:51000/callback"},
		"code_verifier": {"verifier"},
	}
	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Host = "vault.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handlers.HandleTokenProxy(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	want := "https://vault.example.com" + handlers.OAuthCallbackPath
	if got.Get("redirect_uri") != want {
		t.Errorf("upstream redirect_uri = %s, want %s", got.Get("redirect_uri"), want)
	}
	if got.Get("code_verifier") != "verifier" {
		t.Error("expected PKCE verifier to be forwarded untouched")
	}
	if got.Get("client_secret") != "test-secret" {
		t.Error("expected client_secret to be injected")
	}
}

func TestHandleAuthorizeProxy_RequestsOfflineAccess(t *testing.T) {
	q := url.Values{"client_id": {"x"}, "redirect_uri": {"http://localhost:51000/callback"}}
	req := httptest.NewRequest("GET", "/authorize?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	handlers.HandleAuthorizeProxy(oauthConfig()).ServeHTTP(rec, req)

	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("unparseable Location: %v", err)
	}
	if got := loc.Query().Get("access_type"); got != "offline" {
		t.Errorf("access_type = %q, want offline so the provider issues a refresh token", got)
	}
	if got := loc.Query().Get("prompt"); got != "consent" {
		t.Errorf("prompt = %q, want consent so the refresh token is actually handed out", got)
	}
}

func TestHandleAuthorizeProxy_KeepsClientOfflineParams(t *testing.T) {
	q := url.Values{
		"client_id":    {"x"},
		"redirect_uri": {"http://localhost:51000/callback"},
		"access_type":  {"online"},
		"prompt":       {"none"},
	}
	req := httptest.NewRequest("GET", "/authorize?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	handlers.HandleAuthorizeProxy(oauthConfig()).ServeHTTP(rec, req)

	loc, _ := url.Parse(rec.Header().Get("Location"))
	if got := loc.Query().Get("access_type"); got != "online" {
		t.Errorf("access_type = %q, want the client's own value preserved", got)
	}
	if got := loc.Query().Get("prompt"); got != "none" {
		t.Errorf("prompt = %q, want the client's own value preserved", got)
	}
}

func TestHandleTokenProxy_LeavesRefreshGrantAlone(t *testing.T) {
	var got url.Values
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.Form
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"t"}`))
	}))
	defer upstream.Close()

	cfg := oauthConfig()
	cfg.OAuthTokenURL = upstream.URL

	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"r"}}
	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Host = "vault.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handlers.HandleTokenProxy(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got.Has("redirect_uri") {
		t.Errorf("refresh grant must not carry a redirect_uri, got %q", got.Get("redirect_uri"))
	}
	if got.Get("refresh_token") != "r" {
		t.Error("expected the refresh token to be forwarded")
	}
	if got.Get("client_secret") != "test-secret" {
		t.Error("expected client_secret to be injected on refresh too")
	}
}
