package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
		Transport: newWorkspaceRoundTripper(workspaceRTConfig{
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
		Transport: newWorkspaceRoundTripper(workspaceRTConfig{
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

func TestWorkspaceRoundTripper_SkipsWorkspaceManagement(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"create", http.MethodPost, "/api/3.0/mlflow/workspaces"},
		{"get", http.MethodGet, "/api/3.0/mlflow/workspaces/team-x"},
		{"delete", http.MethodDelete, "/api/3.0/mlflow/workspaces/team-x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedHeader string
			var seen bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = true
				receivedHeader = r.Header.Get("X-MLFLOW-WORKSPACE")
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := &http.Client{
				Transport: newWorkspaceRoundTripper(workspaceRTConfig{
					Base:         http.DefaultTransport,
					Workspace:    "team-x",
					ProbeEnabled: true, // must not probe for management calls either
					BaseURL:      server.URL,
				}),
			}

			req, err := http.NewRequest(tt.method, server.URL+tt.path, nil)
			if err != nil {
				t.Fatalf("NewRequest error: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request error: %v", err)
			}
			resp.Body.Close()

			if !seen {
				t.Fatal("server never received the request")
			}
			if receivedHeader != "" {
				t.Errorf("X-MLFLOW-WORKSPACE = %q, want none for management endpoint", receivedHeader)
			}
		})
	}
}

// TestWorkspaceRoundTripper_AttachesToNonManagement is the counterpart: a
// look-alike path that is not workspace management still gets the header.
func TestWorkspaceRoundTripper_AttachesToNonManagement(t *testing.T) {
	var receivedHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("X-MLFLOW-WORKSPACE")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: newWorkspaceRoundTripper(workspaceRTConfig{
			Base:         http.DefaultTransport,
			Workspace:    "team-x",
			ProbeEnabled: false,
			BaseURL:      server.URL,
		}),
	}

	// Not the workspaces prefix (no "/" boundary), so the header must attach.
	resp, err := client.Get(server.URL + "/api/3.0/mlflow/workspaces-summary")
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	resp.Body.Close()

	if receivedHeader != "team-x" {
		t.Errorf("X-MLFLOW-WORKSPACE = %q, want team-x", receivedHeader)
	}
}

// TestWorkspaceRoundTripper_SkipsServerInfo verifies that a direct call to the
// server-info endpoint (e.g. workspace.Client.GetServerInfo) never carries the
// workspace header — the endpoint is workspace-agnostic, and attaching a header
// naming a possibly-disabled or not-yet-created workspace would provoke the very
// FEATURE_DISABLED / RESOURCE_DOES_NOT_EXIST errors GetServerInfo is used to
// detect. With probing enabled the direct call must also not trigger a separate
// probe request.
func TestWorkspaceRoundTripper_SkipsServerInfo(t *testing.T) {
	tests := []struct {
		name         string
		probeEnabled bool
	}{
		{"probe disabled", false},
		{"probe enabled", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits int
			var receivedHeader string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/3.0/mlflow/server-info" {
					hits++
					receivedHeader = r.Header.Get("X-MLFLOW-WORKSPACE")
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"workspaces_enabled": true})
					return
				}
				t.Errorf("unexpected request to %s", r.URL.Path)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := &http.Client{
				Transport: newWorkspaceRoundTripper(workspaceRTConfig{
					Base:         http.DefaultTransport,
					Workspace:    "team-x",
					ProbeEnabled: tt.probeEnabled,
					BaseURL:      server.URL,
				}),
			}

			resp, err := client.Get(server.URL + "/api/3.0/mlflow/server-info")
			if err != nil {
				t.Fatalf("request error: %v", err)
			}
			resp.Body.Close()

			// Exactly the one direct call: the exemption is checked before the
			// probe, so no extra probe request is made.
			if hits != 1 {
				t.Errorf("server-info hits = %d, want 1", hits)
			}
			if receivedHeader != "" {
				t.Errorf("X-MLFLOW-WORKSPACE = %q, want none for server-info endpoint", receivedHeader)
			}
		})
	}
}

func TestWorkspaceRoundTripper_ProbeEnabled_WorkspacesOn(t *testing.T) {
	var apiHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"workspaces_enabled": true})
			return
		}
		apiHeaders = append(apiHeaders, r.Header.Get("X-MLFLOW-WORKSPACE"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: newWorkspaceRoundTripper(workspaceRTConfig{
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
			_ = json.NewEncoder(w).Encode(map[string]any{"workspaces_enabled": false})
			return
		}
		apiHeaders = append(apiHeaders, r.Header.Get("X-MLFLOW-WORKSPACE"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: newWorkspaceRoundTripper(workspaceRTConfig{
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

func TestWorkspaceRoundTripper_ProbeNotFound_CachedDefinitive(t *testing.T) {
	var probeCount int
	var apiHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			probeCount++
			// Pre-workspace server: the route does not exist.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		apiHeaders = append(apiHeaders, r.Header.Get("X-MLFLOW-WORKSPACE"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: newWorkspaceRoundTripper(workspaceRTConfig{
			Base:         http.DefaultTransport,
			Workspace:    "ws",
			ProbeEnabled: true,
			BaseURL:      server.URL,
		}),
	}

	for i := 0; i < 3; i++ {
		resp, err := client.Get(server.URL + "/api/2.0/x")
		if err != nil {
			t.Fatalf("request %d error: %v", i, err)
		}
		resp.Body.Close()
	}

	// A 404 is definitive: probe once, then stop re-probing older servers.
	if probeCount != 1 {
		t.Errorf("expected 404 probe to be cached (1 probe), got %d", probeCount)
	}
	for i, h := range apiHeaders {
		if h != "" {
			t.Errorf("request %d: expected no workspace header, got %q", i, h)
		}
	}
}

func TestWorkspaceRoundTripper_TransientProbeFailure_Retries(t *testing.T) {
	var probeCount int
	var apiHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			probeCount++
			if probeCount == 1 {
				// A transient failure must not be cached permanently.
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"workspaces_enabled": true})
			return
		}
		apiHeaders = append(apiHeaders, r.Header.Get("X-MLFLOW-WORKSPACE"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: newWorkspaceRoundTripper(workspaceRTConfig{
			Base:         http.DefaultTransport,
			Workspace:    "ws",
			ProbeEnabled: true,
			BaseURL:      server.URL,
		}),
	}

	// First request: the probe fails transiently, so the request errors out
	// (rather than silently proceeding) and the failure is not cached.
	resp, err := client.Get(server.URL + "/api/2.0/x")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected first request to error on transient probe failure")
	}

	// Second request: the probe is retried and now succeeds, so the header is
	// attached and the request goes through.
	resp, err = client.Get(server.URL + "/api/2.0/x")
	if err != nil {
		t.Fatalf("second request error: %v", err)
	}
	resp.Body.Close()

	if probeCount != 2 {
		t.Errorf("expected probe to be retried (2 calls), got %d", probeCount)
	}
	// Only the second request reaches the API (the first failed at the probe).
	if len(apiHeaders) != 1 {
		t.Fatalf("expected 1 API call, got %d", len(apiHeaders))
	}
	if apiHeaders[0] != "ws" {
		t.Errorf("expected header after successful retry, got %q", apiHeaders[0])
	}
}

func TestWorkspaceRoundTripper_ProbeCached(t *testing.T) {
	probeCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			probeCount++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"workspaces_enabled": true})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: newWorkspaceRoundTripper(workspaceRTConfig{
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

func TestWorkspaceRoundTripper_ProbeFailure_ReturnsError(t *testing.T) {
	var apiReached bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		apiReached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: newWorkspaceRoundTripper(workspaceRTConfig{
			Base:         http.DefaultTransport,
			Workspace:    "fail-ws",
			ProbeEnabled: true,
			BaseURL:      server.URL,
		}),
	}

	// An inconclusive probe failure must surface as an error rather than
	// silently sending the request without the workspace header.
	resp, err := client.Get(server.URL + "/test")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected an error when the probe fails inconclusively")
	}
	if apiReached {
		t.Error("API request must not be sent when the probe fails inconclusively")
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
		Transport: newWorkspaceRoundTripper(workspaceRTConfig{
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
		{"explicit https default port on base", "https://mlflow.example.com:443", "https://mlflow.example.com/api", true},
		{"explicit https default port on request", "https://mlflow.example.com", "https://mlflow.example.com:443/api", true},
		{"explicit http default port", "http://mlflow.example.com:80", "http://mlflow.example.com/api", true},
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
	rt := newWorkspaceRoundTripper(workspaceRTConfig{
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
	var probeAuth, probeWS, probeAccept, probeContentType string
	var sawProbe bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			sawProbe = true
			probeAuth = r.Header.Get("Authorization")
			probeWS = r.Header.Get("X-MLFLOW-WORKSPACE")
			probeAccept = r.Header.Get("Accept")
			probeContentType = r.Header.Get("Content-Type")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"workspaces_enabled": true})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	rt := newWorkspaceRoundTripper(workspaceRTConfig{
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
	// Entity headers and a non-JSON Accept from the triggering request must not
	// bleed onto the body-less JSON probe.
	req.Header.Set("Accept", "text/csv")
	req.Header.Set("Content-Type", "application/x-protobuf")

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
	if probeAccept != "application/json" {
		t.Errorf("probe Accept = %q, want %q", probeAccept, "application/json")
	}
	if probeContentType != "" {
		t.Errorf("probe must not carry entity Content-Type, got %q", probeContentType)
	}
	if wrt, ok := rt.(*WorkspaceRoundTripper); ok && !wrt.isWorkspacesEnabled() {
		t.Error("isWorkspacesEnabled() = false after successful probe, want true")
	}
}

func TestWorkspaceRoundTripper_ProbeMalformedJSON_ReturnsError(t *testing.T) {
	var apiReached bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{not-json"))
			return
		}
		apiReached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: newWorkspaceRoundTripper(workspaceRTConfig{
			Base:         http.DefaultTransport,
			Workspace:    "ws",
			ProbeEnabled: true,
			BaseURL:      server.URL,
		}),
	}

	resp, err := client.Get(server.URL + "/api/2.0/mlflow/experiments/list")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected an error when the probe response is malformed")
	}
	if apiReached {
		t.Error("API request must not be sent when the probe response is malformed")
	}
}

func TestWorkspaceRoundTripper_ProbeNetworkError_ReturnsError(t *testing.T) {
	var apiCalled bool
	base := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/3.0/mlflow/server-info" {
			return nil, errors.New("probe failed")
		}
		apiCalled = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	})

	rt := newWorkspaceRoundTripper(workspaceRTConfig{
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
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected an error when the probe request fails")
	}
	if apiCalled {
		t.Error("API request must not be sent when the probe request fails")
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

	rt := newWorkspaceRoundTripper(workspaceRTConfig{
		Base:         http.DefaultTransport,
		Workspace:    "ws",
		ProbeEnabled: true,
		BaseURL:      server.URL,
	})
	wrt, ok := rt.(*WorkspaceRoundTripper)
	if !ok {
		t.Fatal("unexpected round-tripper type")
	}

	if wrt.isWorkspacesEnabled() {
		t.Error("isWorkspacesEnabled() = true before probe, want false")
	}
	if err := wrt.forceProbe(); err != nil {
		t.Fatalf("forceProbe error: %v", err)
	}
	if !wrt.isWorkspacesEnabled() {
		t.Error("isWorkspacesEnabled() = false after forceProbe, want true")
	}
}

func TestExtractWorkspaceRT(t *testing.T) {
	if got := extractWorkspaceRT(nil); got != nil {
		t.Error("extractWorkspaceRT(nil) != nil")
	}
	if got := extractWorkspaceRT(&http.Client{}); got != nil {
		t.Error("expected nil for client with no transport")
	}
	if got := extractWorkspaceRT(&http.Client{Transport: http.DefaultTransport}); got != nil {
		t.Error("expected nil for non-workspace transport")
	}

	rt := newWorkspaceRoundTripper(workspaceRTConfig{
		Base:      http.DefaultTransport,
		Workspace: "ws",
		BaseURL:   "http://mlflow.example.com",
	})
	if got := extractWorkspaceRT(&http.Client{Transport: rt}); got == nil {
		t.Error("expected to extract WorkspaceRoundTripper")
	}
}

func TestWrapClientWithWorkspace_InstallsRoundTripper(t *testing.T) {
	original := &http.Client{}
	wrapped := wrapClientWithWorkspace(original, "ws", "http://mlflow.example.com", false)
	if wrapped == original {
		t.Fatal("expected a new client when workspace is set")
	}

	rt := extractWorkspaceRT(wrapped)
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
	result := wrapClientWithWorkspace(original, "", "http://localhost", false)
	if result != original {
		t.Error("expected same client when workspace is empty")
	}
}

// TestWorkspaceRoundTripper_ProbeContextCanceled verifies that a request whose
// context is canceled while another probe holds the probe slot stops waiting
// and returns the context error, rather than blocking on the probe.
func TestWorkspaceRoundTripper_ProbeContextCanceled(t *testing.T) {
	var hits int32
	base := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		atomic.AddInt32(&hits, 1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"workspaces_enabled":true}`)),
			Header:     make(http.Header),
		}, nil
	})
	rt := newWorkspaceRoundTripper(workspaceRTConfig{
		Base:         base,
		Workspace:    "ws",
		ProbeEnabled: true,
		BaseURL:      "http://mlflow.example.com",
	}).(*WorkspaceRoundTripper)

	// Occupy the probe slot so the request below must wait for it.
	rt.probeSem <- struct{}{}

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://mlflow.example.com/api", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, rtErr := rt.RoundTrip(req)
		errCh <- rtErr
	}()

	// The request is blocked waiting for the probe slot; cancel it.
	cancel()

	select {
	case rtErr := <-errCh:
		if !errors.Is(rtErr, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", rtErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RoundTrip did not return after context cancellation")
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("base transport hit %d times, want 0", got)
	}

	<-rt.probeSem // release the slot we occupied
}
