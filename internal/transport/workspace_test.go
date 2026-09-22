package transport

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// roundTripperFunc adapts a function to http.RoundTripper for tests that need
// a fully controllable base transport.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWorkspaceRoundTripper_AlwaysAttach(t *testing.T) {
	var receivedHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("X-MLFLOW-WORKSPACE")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: NewWorkspaceRoundTripper(WorkspaceRTConfig{
			Base:         http.DefaultTransport,
			Workspace:    "my-workspace",
			ProbeEnabled: false,
			BaseURL:      server.URL,
		}),
	}

	resp, err := client.Get(server.URL + "/api/2.0/mlflow/experiments/list")
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	resp.Body.Close()

	if receivedHeader != "my-workspace" {
		t.Errorf("X-MLFLOW-WORKSPACE = %q, want %q", receivedHeader, "my-workspace")
	}
}

func TestWorkspaceRoundTripper_EmptyWorkspace(t *testing.T) {
	var receivedHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("X-MLFLOW-WORKSPACE")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: NewWorkspaceRoundTripper(WorkspaceRTConfig{
			Base:         http.DefaultTransport,
			Workspace:    "",
			ProbeEnabled: false,
			BaseURL:      server.URL,
		}),
	}

	resp, err := client.Get(server.URL + "/test")
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	resp.Body.Close()

	if receivedHeader != "" {
		t.Errorf("expected no workspace header, got %q", receivedHeader)
	}
}

func TestWorkspaceRoundTripper_ProbeEnabled_WorkspacesOn(t *testing.T) {
	var apiHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"workspaces_enabled": true})
			return
		}
		apiHeaders = append(apiHeaders, r.Header.Get("X-MLFLOW-WORKSPACE"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: NewWorkspaceRoundTripper(WorkspaceRTConfig{
			Base:         http.DefaultTransport,
			Workspace:    "probe-ws",
			ProbeEnabled: true,
			BaseURL:      server.URL,
		}),
	}

	resp, err := client.Get(server.URL + "/api/2.0/mlflow/experiments/list")
	if err != nil {
		t.Fatalf("first request error: %v", err)
	}
	resp.Body.Close()

	resp, err = client.Get(server.URL + "/api/2.0/mlflow/experiments/list")
	if err != nil {
		t.Fatalf("second request error: %v", err)
	}
	resp.Body.Close()

	if len(apiHeaders) != 2 {
		t.Fatalf("expected 2 API calls, got %d", len(apiHeaders))
	}
	for i, h := range apiHeaders {
		if h != "probe-ws" {
			t.Errorf("request %d: X-MLFLOW-WORKSPACE = %q, want %q", i+1, h, "probe-ws")
		}
	}
}

func TestWorkspaceRoundTripper_ProbeEnabled_WorkspacesOff(t *testing.T) {
	var apiHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"workspaces_enabled": false})
			return
		}
		apiHeaders = append(apiHeaders, r.Header.Get("X-MLFLOW-WORKSPACE"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: NewWorkspaceRoundTripper(WorkspaceRTConfig{
			Base:         http.DefaultTransport,
			Workspace:    "my-ws",
			ProbeEnabled: true,
			BaseURL:      server.URL,
		}),
	}

	resp, err := client.Get(server.URL + "/api/2.0/mlflow/experiments/list")
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	resp.Body.Close()

	if len(apiHeaders) != 1 {
		t.Fatalf("expected 1 API call, got %d", len(apiHeaders))
	}
	if apiHeaders[0] != "" {
		t.Errorf("expected no workspace header when disabled, got %q", apiHeaders[0])
	}
}

func TestWorkspaceRoundTripper_ProbeCached(t *testing.T) {
	probeCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			probeCount++
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"workspaces_enabled": true})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: NewWorkspaceRoundTripper(WorkspaceRTConfig{
			Base:         http.DefaultTransport,
			Workspace:    "cached-ws",
			ProbeEnabled: true,
			BaseURL:      server.URL,
		}),
	}

	for i := 0; i < 5; i++ {
		resp, err := client.Get(server.URL + "/test")
		if err != nil {
			t.Fatalf("request %d error: %v", i, err)
		}
		resp.Body.Close()
	}

	if probeCount != 1 {
		t.Errorf("expected 1 probe call, got %d", probeCount)
	}
}

func TestWorkspaceRoundTripper_ProbeFailure_SkipsHeader(t *testing.T) {
	var receivedHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		receivedHeader = r.Header.Get("X-MLFLOW-WORKSPACE")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: NewWorkspaceRoundTripper(WorkspaceRTConfig{
			Base:         http.DefaultTransport,
			Workspace:    "fail-ws",
			ProbeEnabled: true,
			BaseURL:      server.URL,
		}),
	}

	resp, err := client.Get(server.URL + "/test")
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	resp.Body.Close()

	if receivedHeader != "" {
		t.Errorf("expected no workspace header on probe failure, got %q", receivedHeader)
	}
}

func TestWorkspaceRoundTripper_CrossOriginRedirect(t *testing.T) {
	var otherHeader string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHeader = r.Header.Get("X-MLFLOW-WORKSPACE")
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()

	var originHeader string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHeader = r.Header.Get("X-MLFLOW-WORKSPACE")
		http.Redirect(w, r, other.URL+"/redirected", http.StatusFound)
	}))
	defer origin.Close()

	client := &http.Client{
		Transport: NewWorkspaceRoundTripper(WorkspaceRTConfig{
			Base:         http.DefaultTransport,
			Workspace:    "my-workspace",
			ProbeEnabled: false,
			BaseURL:      origin.URL,
		}),
	}

	resp, err := client.Get(origin.URL + "/api/2.0/mlflow/experiments/list")
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	resp.Body.Close()

	if originHeader != "my-workspace" {
		t.Errorf("origin request X-MLFLOW-WORKSPACE = %q, want %q", originHeader, "my-workspace")
	}
	if otherHeader != "" {
		t.Errorf("cross-origin redirect must not receive workspace header, got %q", otherHeader)
	}
}

func TestWorkspaceRoundTripper_SameOrigin(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		reqURL  string
		want    bool
	}{
		{"identical origin", "https://mlflow.example.com", "https://mlflow.example.com/api/2.0/x", true},
		{"case-insensitive host", "https://MLflow.Example.COM", "https://mlflow.example.com/api", true},
		{"different host", "https://mlflow.example.com", "https://evil.example.com/api", false},
		{"different scheme", "https://mlflow.example.com", "http://mlflow.example.com/api", false},
		{"different port", "https://mlflow.example.com:8443", "https://mlflow.example.com/api", false},
		{"empty base URL", "", "https://mlflow.example.com/api", false},
		{"unparseable base URL", "://bad", "https://mlflow.example.com/api", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &WorkspaceRoundTripper{workspace: "ws", baseURL: tt.baseURL}
			req, err := http.NewRequest(http.MethodGet, tt.reqURL, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			if got := w.sameOrigin(req); got != tt.want {
				t.Errorf("sameOrigin() = %v, want %v", got, tt.want)
			}
		})
	}

	w := &WorkspaceRoundTripper{workspace: "ws", baseURL: "https://mlflow.example.com"}
	if w.sameOrigin(nil) {
		t.Error("sameOrigin(nil) = true, want false")
	}
	if w.sameOrigin(&http.Request{}) {
		t.Error("sameOrigin(request with nil URL) = true, want false")
	}
}

func TestWorkspaceRoundTripper_StripsCallerHeaderWhenNotAttached(t *testing.T) {
	var forwarded string
	base := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		forwarded = r.Header.Get("X-MLFLOW-WORKSPACE")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	})

	// Cross-origin request: attachment is not allowed, so a caller-supplied
	// workspace header must be stripped rather than leaked.
	rt := NewWorkspaceRoundTripper(WorkspaceRTConfig{
		Base:      base,
		Workspace: "ws",
		BaseURL:   "https://mlflow.example.com",
	})
	req, err := http.NewRequest(http.MethodGet, "https://evil.example.com/api", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-MLFLOW-WORKSPACE", "leaked")

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip error: %v", err)
	}
	resp.Body.Close()

	if forwarded != "" {
		t.Errorf("cross-origin request forwarded workspace header %q, want stripped", forwarded)
	}
	// The caller's original request must not be mutated.
	if req.Header.Get("X-MLFLOW-WORKSPACE") != "leaked" {
		t.Error("caller request header was mutated")
	}
}

func TestWorkspaceRoundTripper_ProbeForwardsHeaders(t *testing.T) {
	var probeAuth, probeWS string
	var sawProbe bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			sawProbe = true
			probeAuth = r.Header.Get("Authorization")
			probeWS = r.Header.Get("X-MLFLOW-WORKSPACE")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"workspaces_enabled": true})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	rt := NewWorkspaceRoundTripper(WorkspaceRTConfig{
		Base:         http.DefaultTransport,
		Workspace:    "ws",
		ProbeEnabled: true,
		BaseURL:      server.URL,
	})
	client := &http.Client{Transport: rt}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/2.0/mlflow/experiments/list", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer token123")
	// A caller-supplied workspace header must never be forwarded to the probe.
	req.Header.Set("X-MLFLOW-WORKSPACE", "caller-value")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	resp.Body.Close()

	if !sawProbe {
		t.Fatal("probe was not triggered")
	}
	if probeAuth != "Bearer token123" {
		t.Errorf("probe Authorization = %q, want forwarded %q", probeAuth, "Bearer token123")
	}
	if probeWS != "" {
		t.Errorf("probe must not carry workspace header, got %q", probeWS)
	}
	if wrt, ok := rt.(*WorkspaceRoundTripper); ok && !wrt.IsWorkspacesEnabled() {
		t.Error("IsWorkspacesEnabled() = false after successful probe, want true")
	}
}

func TestWorkspaceRoundTripper_ProbeMalformedJSON_SkipsHeader(t *testing.T) {
	var apiHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{not-json"))
			return
		}
		apiHeader = r.Header.Get("X-MLFLOW-WORKSPACE")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: NewWorkspaceRoundTripper(WorkspaceRTConfig{
			Base:         http.DefaultTransport,
			Workspace:    "ws",
			ProbeEnabled: true,
			BaseURL:      server.URL,
		}),
	}

	resp, err := client.Get(server.URL + "/api/2.0/mlflow/experiments/list")
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	resp.Body.Close()

	if apiHeader != "" {
		t.Errorf("expected no workspace header on malformed probe JSON, got %q", apiHeader)
	}
}

func TestWorkspaceRoundTripper_ProbeNetworkError_SkipsHeader(t *testing.T) {
	var apiCalled bool
	var apiHeader string
	base := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			return nil, errors.New("probe failed")
		}
		apiCalled = true
		apiHeader = r.Header.Get("X-MLFLOW-WORKSPACE")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	})

	rt := NewWorkspaceRoundTripper(WorkspaceRTConfig{
		Base:         base,
		Workspace:    "ws",
		ProbeEnabled: true,
		BaseURL:      "http://mlflow.example.com",
	})

	req, err := http.NewRequest(http.MethodGet, "http://mlflow.example.com/api/2.0/x", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip error: %v", err)
	}
	resp.Body.Close()

	if !apiCalled {
		t.Fatal("API request was not made")
	}
	if apiHeader != "" {
		t.Errorf("expected no workspace header on probe network error, got %q", apiHeader)
	}
}

func TestWorkspaceRoundTripper_ForceProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"workspaces_enabled": true})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	rt := NewWorkspaceRoundTripper(WorkspaceRTConfig{
		Base:         http.DefaultTransport,
		Workspace:    "ws",
		ProbeEnabled: true,
		BaseURL:      server.URL,
	})
	wrt, ok := rt.(*WorkspaceRoundTripper)
	if !ok {
		t.Fatal("unexpected round-tripper type")
	}

	if wrt.IsWorkspacesEnabled() {
		t.Error("IsWorkspacesEnabled() = true before probe, want false")
	}
	wrt.ForceProbe()
	if !wrt.IsWorkspacesEnabled() {
		t.Error("IsWorkspacesEnabled() = false after ForceProbe, want true")
	}
}

func TestExtractWorkspaceRT(t *testing.T) {
	if got := ExtractWorkspaceRT(nil); got != nil {
		t.Error("ExtractWorkspaceRT(nil) != nil")
	}
	if got := ExtractWorkspaceRT(&http.Client{}); got != nil {
		t.Error("expected nil for client with no transport")
	}
	if got := ExtractWorkspaceRT(&http.Client{Transport: http.DefaultTransport}); got != nil {
		t.Error("expected nil for non-workspace transport")
	}

	rt := NewWorkspaceRoundTripper(WorkspaceRTConfig{
		Base:      http.DefaultTransport,
		Workspace: "ws",
		BaseURL:   "http://mlflow.example.com",
	})
	if got := ExtractWorkspaceRT(&http.Client{Transport: rt}); got == nil {
		t.Error("expected to extract WorkspaceRoundTripper")
	}

	if WorkspaceHeaderValue() != "X-MLFLOW-WORKSPACE" {
		t.Errorf("WorkspaceHeaderValue() = %q, want %q", WorkspaceHeaderValue(), "X-MLFLOW-WORKSPACE")
	}
}

func TestEnsureWorkspaceHeader(t *testing.T) {
	if got := EnsureWorkspaceHeader(nil, ""); got != nil {
		t.Errorf("empty workspace: got %v, want nil", got)
	}

	got := EnsureWorkspaceHeader(nil, "ws")
	if got["X-MLFLOW-WORKSPACE"] != "ws" {
		t.Errorf("nil map: header = %q, want %q", got["X-MLFLOW-WORKSPACE"], "ws")
	}

	in := map[string]string{"Authorization": "Bearer t"}
	got = EnsureWorkspaceHeader(in, "ws2")
	if got["Authorization"] != "Bearer t" {
		t.Errorf("existing header dropped: %v", got)
	}
	if got["X-MLFLOW-WORKSPACE"] != "ws2" {
		t.Errorf("workspace header = %q, want %q", got["X-MLFLOW-WORKSPACE"], "ws2")
	}
}

func TestWrapClientWithWorkspace_InstallsRoundTripper(t *testing.T) {
	original := &http.Client{}
	wrapped := WrapClientWithWorkspace(original, "ws", "http://mlflow.example.com", false)
	if wrapped == original {
		t.Fatal("expected a new client when workspace is set")
	}

	rt := ExtractWorkspaceRT(wrapped)
	if rt == nil {
		t.Fatal("expected WorkspaceRoundTripper to be installed")
	}
	// A nil source transport must fall back to http.DefaultTransport as the base.
	if rt.base != http.DefaultTransport {
		t.Error("expected base to fall back to http.DefaultTransport")
	}
}

func TestWrapClientWithWorkspace_NoWorkspace(t *testing.T) {
	original := &http.Client{}
	result := WrapClientWithWorkspace(original, "", "http://localhost", false)
	if result != original {
		t.Error("expected same client when workspace is empty")
	}
}
