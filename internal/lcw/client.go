package lcw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the only host this API uses.
const DefaultBaseURL = "https://api.livecoinwatch.com"

// maxErrorSnippet caps how much of a non-JSON body is kept for diagnosis.
const maxErrorSnippet = 200

// Client talks to Live Coin Watch. It knows about HTTP and nothing else: no
// caching, no credit accounting, no retries. Those belong to callers, because
// only they know whether a given request is worth a credit.
type Client struct {
	baseURL   string
	apiKey    string
	userAgent string
	http      *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API host. Used by tests to point at an httptest
// server.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient supplies the underlying client, for tests and for tuning
// transport limits.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// WithTimeout sets the per-request timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.http.Timeout = d }
}

// New builds a Client. An empty apiKey is allowed: the program must start and
// serve a UI that explains how to configure the key, so every keyed call
// returns ErrNoAPIKey without touching the network.
func New(apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL:   DefaultBaseURL,
		apiKey:    apiKey,
		userAgent: "lcw-dashboard",
		http:      &http.Client{Timeout: 15 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// HasKey reports whether a key is configured.
func (c *Client) HasKey() bool { return c.apiKey != "" }

// post sends body to endpoint and decodes the response into out.
//
// needsKey distinguishes /status, which works unauthenticated, from everything
// else. When out is nil the body is discarded after the error check, which is
// what /status wants.
func (c *Client) post(ctx context.Context, endpoint string, needsKey bool, body, out any) error {
	if needsKey && c.apiKey == "" {
		return ErrNoAPIKey
	}

	var payload io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request for %s: %w", endpoint, err)
		}
		payload = bytes.NewReader(buf)
	}

	// POST is the only method this API accepts; GET returns 405 everywhere.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpoint, payload)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", endpoint, err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("user-agent", c.userAgent)
	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// The request never reached the API, so it cost no credit. Callers rely
		// on this distinction to decide whether to refund a reservation.
		return fmt.Errorf("request %s: %w", endpoint, err)
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("read %s response: %w", endpoint, err)
	}

	if !looksLikeJSON(raw) {
		return &ErrNonJSON{
			Endpoint:    endpoint,
			ContentType: resp.Header.Get("content-type"),
			StatusCode:  resp.StatusCode,
			Snippet:     snippet(raw),
		}
	}

	// Check for an API error object before looking at the status code. This API
	// returns errors under HTTP 200, so trusting the status first would accept
	// an error body as data.
	var probe struct {
		Error *APIError `json:"error"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil && probe.Error != nil {
		probe.Error.HTTPStatus = resp.StatusCode
		probe.Error.Endpoint = endpoint
		return probe.Error
	}

	// No error object, but a failing status still means failure — synthesise an
	// APIError so callers have one error type to classify.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{
			Code:        resp.StatusCode,
			Status:      http.StatusText(resp.StatusCode),
			Description: snippet(raw),
			HTTPStatus:  resp.StatusCode,
			Endpoint:    endpoint,
		}
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s response: %w", endpoint, err)
	}
	return nil
}

// looksLikeJSON reports whether the body starts with a JSON object or array. A
// content-type check alone is not enough: proxies and captive portals routinely
// serve HTML while claiming application/json.
func looksLikeJSON(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false // empty body
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > maxErrorSnippet {
		return s[:maxErrorSnippet] + "…"
	}
	return s
}
