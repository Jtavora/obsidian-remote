package obsidian

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/shanehull/obsidian-remote/internal/config"
)

type Client struct {
	cfg  *config.Config
	http *http.Client
}

func NewClient(cfg *config.Config) *Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // internal loopback only
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Transport: tr},
	}
}

func (c *Client) Call(method, path string, body []byte, headers ...map[string]string) ([]byte, error) {
	req, err := http.NewRequest(method, c.cfg.ObsidianURL+path, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.cfg.ObsidianKey)
	req.Header.Set("Content-Type", contentTypeFor(method, path))

	for _, h := range headers {
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, &APIError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: respBody}
	}

	return respBody, nil
}

// APIError is a non-2xx response from the Obsidian Local REST API.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       []byte
}

func (e *APIError) Error() string {
	return fmt.Sprintf("obsidian API %s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// errorCodeMissingTargetTypeHeader is what the Local REST API returns when a PATCH
// carries no Target-Type header. Plugin versions older than the JSON request body
// support only header-based targeting, so they reject our JSON body with this code.
const errorCodeMissingTargetTypeHeader = 40053

// LegacyTargetingRequired reports whether err came from a plugin that only
// understands header-based targeting, meaning the PATCH is worth retrying with
// the legacy headers.
func LegacyTargetingRequired(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		return false
	}
	var payload struct {
		ErrorCode int    `json:"errorCode"`
		Message   string `json:"message"`
	}
	if json.Unmarshal(apiErr.Body, &payload) != nil {
		return false
	}
	return payload.ErrorCode == errorCodeMissingTargetTypeHeader ||
		strings.Contains(payload.Message, "Target-Type")
}

// contentTypeFor returns the appropriate Content-Type for an Obsidian REST API request.
// Vault write operations expect text/markdown; everything else uses application/json.
func contentTypeFor(method, path string) string {
	if strings.HasPrefix(path, "/vault/") && (method == http.MethodPut || method == http.MethodPost || method == http.MethodPatch) {
		return "text/markdown"
	}
	return "application/json"
}
