package transport

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

const workspaceHeader = "X-MLFLOW-WORKSPACE"

// WorkspaceRoundTripper conditionally attaches the X-MLFLOW-WORKSPACE header.
// When probeEnabled is true, the first request triggers a probe to
// GET /api/3.0/mlflow/server-info to check if workspaces are enabled;
// the result is cached for all subsequent requests. When probeEnabled
// is false, the header is always attached.
type WorkspaceRoundTripper struct {
	base          http.RoundTripper
	workspace     string
	probeEnabled  bool
	baseURL       string

	probeOnce sync.Once
	enabled   bool
}

// WorkspaceRTConfig configures a WorkspaceRoundTripper.
type WorkspaceRTConfig struct {
	Base         http.RoundTripper
	Workspace    string
	ProbeEnabled bool
	BaseURL      string
}

// NewWorkspaceRoundTripper creates a round-tripper that conditionally
// attaches the workspace header.
func NewWorkspaceRoundTripper(cfg WorkspaceRTConfig) http.RoundTripper {
	return &WorkspaceRoundTripper{
		base:         cfg.Base,
		workspace:    cfg.Workspace,
		probeEnabled: cfg.ProbeEnabled,
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
	}
}

func (w *WorkspaceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if w.shouldAttach(req) {
		r := req.Clone(req.Context())
		r.Header.Set(workspaceHeader, w.workspace)
		return w.base.RoundTrip(r)
	}
	return w.base.RoundTrip(req)
}

func (w *WorkspaceRoundTripper) shouldAttach(req *http.Request) bool {
	if w.workspace == "" {
		return false
	}
	if !w.probeEnabled {
		return true
	}

	w.probeOnce.Do(func() {
		w.enabled = w.probeWorkspaces(req)
	})
	return w.enabled
}

func (w *WorkspaceRoundTripper) probeWorkspaces(original *http.Request) bool {
	probeURL := w.baseURL + "/api/3.0/mlflow/server-info"
	req, err := http.NewRequestWithContext(original.Context(), http.MethodGet, probeURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range original.Header {
		if strings.EqualFold(k, workspaceHeader) {
			continue
		}
		req.Header[k] = v
	}

	resp, err := w.base.RoundTrip(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false
	}

	var info struct {
		WorkspacesEnabled bool `json:"workspaces_enabled"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return false
	}
	return info.WorkspacesEnabled
}

// IsWorkspacesEnabled returns whether the probe detected workspaces support.
// Returns false if probing has not yet occurred or if probing is disabled.
func (w *WorkspaceRoundTripper) IsWorkspacesEnabled() bool {
	return w.enabled
}

// ForceProbe triggers the workspace probe immediately, using the provided
// base URL to construct the probe request. This is useful for testing.
func (w *WorkspaceRoundTripper) ForceProbe() {
	req, err := http.NewRequest(http.MethodGet, w.baseURL+"/", nil) //nolint:noctx // probe helper
	if err != nil {
		return
	}
	w.probeOnce.Do(func() {
		w.enabled = w.probeWorkspaces(req)
	})
}

// WrapClientWithWorkspace returns a shallow copy of the HTTP client with
// the workspace round-tripper installed, or the original client if no
// workspace is configured.
func WrapClientWithWorkspace(c *http.Client, workspace, baseURL string, probeEnabled bool) *http.Client {
	if workspace == "" {
		return c
	}
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone := *c
	clone.Transport = NewWorkspaceRoundTripper(WorkspaceRTConfig{
		Base:         base,
		Workspace:    workspace,
		ProbeEnabled: probeEnabled,
		BaseURL:      baseURL,
	})
	return &clone
}

// ExtractWorkspaceRT returns the *WorkspaceRoundTripper from the given
// http.Client's transport chain, if present. Returns nil otherwise.
func ExtractWorkspaceRT(c *http.Client) *WorkspaceRoundTripper {
	if c == nil || c.Transport == nil {
		return nil
	}
	if wrt, ok := c.Transport.(*WorkspaceRoundTripper); ok {
		return wrt
	}
	return nil
}

// EnsureWorkspaceHeader sets the X-MLFLOW-WORKSPACE header on the client's
// static headers map if workspace probing is not enabled. This is used as
// a fallback for clients that do not use the round-tripper approach.
func EnsureWorkspaceHeader(headers map[string]string, workspace string) map[string]string {
	if workspace == "" {
		return headers
	}
	if headers == nil {
		headers = make(map[string]string)
	}
	headers[workspaceHeader] = workspace
	return headers
}

// WorkspaceHeaderValue is exported for callers that need the canonical header name.
func WorkspaceHeaderValue() string {
	return workspaceHeader
}
