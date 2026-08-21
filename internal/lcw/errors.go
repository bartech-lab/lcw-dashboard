package lcw

import (
	"errors"
	"fmt"
	"net/http"
)

// APIError is an error object returned by Live Coin Watch.
//
// The API can return one of these under an HTTP 200, so the client checks for
// the error key before trusting the status code. Treating a 200 as success is
// the single easiest way to silently accept garbage from this API.
type APIError struct {
	Code        int    `json:"code"`
	Status      string `json:"status"`
	Description string `json:"description"`

	// HTTPStatus is the transport status that carried the error, recorded
	// because it frequently disagrees with Code.
	HTTPStatus int `json:"-"`
	// Endpoint is the path that produced the error, for logs.
	Endpoint string `json:"-"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("livecoinwatch %s: %d %s: %s", e.Endpoint, e.Code, e.Status, e.Description)
}

// IsAuth reports an API key problem: absent, wrong, or revoked. Retrying does
// not help, so the caller stops polling instead of hammering upstream.
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

// IsCreditExhausted reports that the daily credit allowance is spent. The
// caller adopts this as truth immediately, overriding its local ledger, and
// waits for UTC midnight.
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

// IsServerSide reports a 5xx or a maintenance response — transient, so the
// circuit breaker backs off and retries rather than giving up.
func IsServerSide(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	return e.Code >= 500 || e.HTTPStatus >= 500
}

// ErrNonJSON reports a response that was not JSON at all — typically a captive
// portal, a proxy error page, or an HTML maintenance notice. Kept distinct from
// a parse failure so logs distinguish "wrong content type entirely" from
// "JSON we could not fit to our struct".
type ErrNonJSON struct {
	Endpoint    string
	ContentType string
	StatusCode  int
	Snippet     string // first bytes of the body, for diagnosis
}

func (e *ErrNonJSON) Error() string {
	return fmt.Sprintf("livecoinwatch %s: non-JSON response (http %d, content-type %q): %s",
		e.Endpoint, e.StatusCode, e.ContentType, e.Snippet)
}

// ErrNoAPIKey is returned before any request is attempted when the key is
// missing. It never reaches the network, so it costs no credit and must not be
// counted as a spend.
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
