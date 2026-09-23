package transport

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
)

const workspaceHeader = "X-MLFLOW-WORKSPACE"

// WorkspaceRoundTripper conditionally attaches the X-MLFLOW-WORKSPACE header.
// When probeEnabled is true, the first request triggers a probe to
// GET /api/3.0/mlflow/server-info to check if workspaces are enabled;
// the result is cached for all subsequent requests. When probeEnabled
// is false, the header is always attached.
type WorkspaceRoundTripper struct {
	base         http.RoundTripper
	workspace    string
	probeEnabled bool
	baseURL      string

	// probeSem is a capacity-1 semaphore guarding the one-shot probe. Acquiring
	// it via a select also honors request cancellation, so a canceled request
	// does not block waiting for an in-flight probe. Buffered-channel
	// happens-before ordering makes the plain probed bool safe to read and write
	// while the slot is held.
	probeSem chan struct{}
	probed   bool
	enabled  atomic.Bool
}

// workspaceRTConfig configures a WorkspaceRoundTripper.
type workspaceRTConfig struct {
	Base         http.RoundTripper
	Workspace    string
	ProbeEnabled bool
	BaseURL      string
}

// newWorkspaceRoundTripper creates a round-tripper that conditionally
// attaches the workspace header.
func newWorkspaceRoundTripper(cfg workspaceRTConfig) http.RoundTripper {
	return &WorkspaceRoundTripper{
		base:         cfg.Base,
		workspace:    cfg.Workspace,
		probeEnabled: cfg.ProbeEnabled,
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		probeSem:     make(chan struct{}, 1),
	}
}

func (w *WorkspaceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone so the caller's request is never mutated, and drop any inbound
	// workspace header so we never forward one we did not sanction (e.g. a
	// caller-supplied header on a cross-origin request). The header is
	// re-added below only when attachment is allowed for this request.
	r := req.Clone(req.Context())
	r.Header.Del(workspaceHeader)
	attach, err := w.shouldAttach(req)
	if err != nil {
		// A workspace was configured but the probe could not determine
		// whether the server supports it. Fail loudly rather than silently
		// sending the request without the header, which could land writes in
		// the default workspace by mistake. The base RoundTripper is never
		// reached on this path, so close the request body ourselves to honor
		// the RoundTripper contract and avoid leaking it.
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	if attach {
		r.Header.Set(workspaceHeader, w.workspace)
	}
	return w.base.RoundTrip(r)
}

func (w *WorkspaceRoundTripper) shouldAttach(req *http.Request) (bool, error) {
	if w.workspace == "" {
		return false, nil
	}
	// Only attach the workspace header to requests aimed at the configured
	// origin. This guards against leaking the header to a different host if
	// the client follows a cross-origin redirect.
	if !w.sameOrigin(req) {
		return false, nil
	}
	if !w.probeEnabled {
		return true, nil
	}
	return w.ensureProbed(req)
}

// ensureProbed returns whether workspaces are supported, probing once for a
// definitive answer. A definitive result (the server returned a parsable
// server-info payload, or reported the route as absent) is cached for the
// lifetime of the client. An inconclusive failure — network error, transient
// status, read error, malformed JSON — is not cached and is returned as an
// error so the caller can surface it and retry, rather than silently
// proceeding without the workspace header.
func (w *WorkspaceRoundTripper) ensureProbed(req *http.Request) (bool, error) {
	// Acquire the probe slot, but honor request cancellation so a canceled
	// request stops waiting for an in-flight probe instead of blocking on it.
	select {
	case w.probeSem <- struct{}{}:
		defer func() { <-w.probeSem }()
	case <-req.Context().Done():
		return false, req.Context().Err()
	}
	if w.probed {
		return w.enabled.Load(), nil
	}
	supported, definitive, err := w.probeWorkspaces(req)
	if !definitive {
		return false, fmt.Errorf("workspace probe failed: %w", err)
	}
	w.probed = true
	w.enabled.Store(supported)
	return supported, nil
}

// sameOrigin reports whether req targets the same origin as the configured
// base URL. Scheme and host are compared case-insensitively, and the default
// port for the scheme is normalized so that, e.g., https://example.com and
// https://example.com:443 are treated as the same origin. Requests with a
// missing URL, or a base URL that cannot be parsed or has no host, are treated
// as cross-origin and rejected.
func (w *WorkspaceRoundTripper) sameOrigin(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	base, err := url.Parse(w.baseURL)
	if err != nil || base.Host == "" {
		return false
	}
	return strings.EqualFold(req.URL.Scheme, base.Scheme) &&
		strings.EqualFold(canonicalHostPort(req.URL), canonicalHostPort(base))
}

// canonicalHostPort returns host:port for u, substituting the scheme's default
// port when none is given, so origins that differ only by an explicit default
// port compare equal.
func canonicalHostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return u.Hostname() + ":" + port
}

// probeWorkspaces asks the server whether workspaces are enabled. It returns
// (supported, definitive, err): definitive is true when the server gave a
// conclusive answer — a parsable server-info payload, or a status indicating
// the route is absent. An inconclusive failure returns (false, false, err) so
// the caller can surface the error and retry.
func (w *WorkspaceRoundTripper) probeWorkspaces(original *http.Request) (supported, definitive bool, err error) {
	probeURL := w.baseURL + "/api/3.0/mlflow/server-info"
	req, err := http.NewRequestWithContext(original.Context(), http.MethodGet, probeURL, nil)
	if err != nil {
		return false, false, fmt.Errorf("build probe request: %w", err)
	}
	// Forward the triggering request's headers (e.g. Authorization, Cookie) so
	// the probe is authenticated, but skip the workspace header and any
	// entity/body headers that don't apply to this body-less GET. Accept is set
	// last so it always wins over an inherited value.
	for k, v := range original.Header {
		switch {
		case strings.EqualFold(k, workspaceHeader),
			strings.EqualFold(k, "Content-Type"),
			strings.EqualFold(k, "Content-Length"),
			strings.EqualFold(k, "Transfer-Encoding"),
			strings.EqualFold(k, "Expect"):
			continue
		}
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")

	resp, err := w.base.RoundTrip(req)
	if err != nil {
		return false, false, fmt.Errorf("probe request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
			// The server has no server-info route, so it predates the
			// workspace feature: workspaces are definitively unsupported.
			// Cache this so we stop probing older servers on every request.
			return false, true, nil
		}
		// Any other status may be transient (e.g. 5xx, auth hiccup); retry.
		return false, false, fmt.Errorf("probe returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, false, fmt.Errorf("read probe response: %w", err)
	}

	var info struct {
		WorkspacesEnabled bool `json:"workspaces_enabled"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return false, false, fmt.Errorf("parse probe response: %w", err)
	}
	return info.WorkspacesEnabled, true, nil
}

// isWorkspacesEnabled returns whether the probe detected workspaces support.
// Returns false if probing has not yet occurred or if probing is disabled.
func (w *WorkspaceRoundTripper) isWorkspacesEnabled() bool {
	return w.enabled.Load()
}

// forceProbe triggers the workspace probe immediately, using the configured
// base URL to construct the probe request. It returns any inconclusive probe
// error. This is useful for testing.
func (w *WorkspaceRoundTripper) forceProbe() error {
	req, err := http.NewRequest(http.MethodGet, w.baseURL+"/", nil) //nolint:noctx // probe helper
	if err != nil {
		return err
	}
	_, err = w.ensureProbed(req)
	return err
}

// wrapClientWithWorkspace returns a shallow copy of the HTTP client with
// the workspace round-tripper installed, or the original client if no
// workspace is configured.
func wrapClientWithWorkspace(c *http.Client, workspace, baseURL string, probeEnabled bool) *http.Client {
	if workspace == "" {
		return c
	}
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone := *c
	clone.Transport = newWorkspaceRoundTripper(workspaceRTConfig{
		Base:         base,
		Workspace:    workspace,
		ProbeEnabled: probeEnabled,
		BaseURL:      baseURL,
	})
	return &clone
}

// extractWorkspaceRT returns the *WorkspaceRoundTripper from the given
// http.Client's transport chain, if present. Returns nil otherwise.
func extractWorkspaceRT(c *http.Client) *WorkspaceRoundTripper {
	if c == nil || c.Transport == nil {
		return nil
	}
	if wrt, ok := c.Transport.(*WorkspaceRoundTripper); ok {
		return wrt
	}
	return nil
}
