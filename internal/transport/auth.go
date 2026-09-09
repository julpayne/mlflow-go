package transport

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// FormatAuthHeader returns the Authorization header value for a token.
// Tokens containing a colon are treated as user:password and encoded as Basic auth;
// all others are sent as Bearer tokens.
func FormatAuthHeader(token string) string {
	if strings.Contains(token, ":") {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(token))
	}
	return "Bearer " + token
}

// tokenRoundTripper injects a static Authorization header on every request.
type tokenRoundTripper struct {
	base      http.RoundTripper
	authValue string
}

// NewTokenRoundTripper wraps base to inject an Authorization header with the given token.
func NewTokenRoundTripper(base http.RoundTripper, token string) http.RoundTripper {
	return &tokenRoundTripper{
		base:      base,
		authValue: FormatAuthHeader(token),
	}
}

func (t *tokenRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", t.authValue)
	return t.base.RoundTrip(r)
}

// tokenFileRoundTripper re-reads a token file on every request to support
// Kubernetes projected service-account tokens that rotate.
type tokenFileRoundTripper struct {
	base      http.RoundTripper
	tokenPath string
}

// NewTokenFileRoundTripper wraps base to read the token from path on every request.
func NewTokenFileRoundTripper(base http.RoundTripper, path string) http.RoundTripper {
	return &tokenFileRoundTripper{
		base:      base,
		tokenPath: path,
	}
}

func (t *tokenFileRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	data, err := os.ReadFile(t.tokenPath)
	if err != nil {
		return nil, fmt.Errorf("mlflow: failed to read token file %q: %w", t.tokenPath, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return nil, fmt.Errorf("mlflow: token file %q is empty", t.tokenPath)
	}

	r := req.Clone(req.Context())
	r.Header.Set("Authorization", FormatAuthHeader(token))
	return t.base.RoundTrip(r)
}
