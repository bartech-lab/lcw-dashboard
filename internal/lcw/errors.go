package lcw

import (
	"errors"
	"fmt"
	"net/http"
)

// APIError is a Live Coin Watch error object. It can arrive under HTTP 200, so
// HTTPStatus is recorded separately from Code.
type APIError struct {
	Code        int    `json:"code"`
	Status      string `json:"status"`
	Description string `json:"description"`

	HTTPStatus int    `json:"-"`
	Endpoint   string `json:"-"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("livecoinwatch %s: %d %s: %s", e.Endpoint, e.Code, e.Status, e.Description)
}

// IsAuth reports a key problem. Retrying does not help, so the caller stops
// polling instead of hammering upstream.
func IsAuth(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	return e.Code == http.StatusUnauthorized ||
		e.Code == http.StatusForbidden ||
		e.HTTPStatus == http.StatusUnauthorized ||
		e.HTTPStatus == http.StatusForbidden
}

// IsCreditExhausted reports the daily allowance is spent. Callers adopt this
// over their local ledger.
func IsCreditExhausted(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	return e.Code == http.StatusTooManyRequests ||
		e.Code == http.StatusPaymentRequired ||
		e.HTTPStatus == http.StatusTooManyRequests ||
		containsFold(e.Description, "credit") ||
		containsFold(e.Status, "credit")
}

func IsServerSide(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	return e.Code >= 500 || e.HTTPStatus >= 500
}

// ErrNonJSON is a response that was not JSON at all: captive portal, proxy
// error page, HTML maintenance notice.
type ErrNonJSON struct {
	Endpoint    string
	ContentType string
	StatusCode  int
	Snippet     string
}

func (e *ErrNonJSON) Error() string {
	return fmt.Sprintf("livecoinwatch %s: non-JSON response (http %d, content-type %q): %s",
		e.Endpoint, e.StatusCode, e.ContentType, e.Snippet)
}

// ErrNoAPIKey never reaches the network, so it must not be counted as a spend.
var ErrNoAPIKey = errors.New("no Live Coin Watch API key configured")

func containsFold(haystack, needle string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if equalFold(haystack[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	for i := 0; i < len(a); i++ {
		if lower(a[i]) != lower(b[i]) {
			return false
		}
	}
	return true
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}
