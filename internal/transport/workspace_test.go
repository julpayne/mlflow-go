package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestWrapClientWithWorkspace_NoWorkspace(t *testing.T) {
	original := &http.Client{}
	result := WrapClientWithWorkspace(original, "", "http://localhost", false)
	if result != original {
		t.Error("expected same client when workspace is empty")
	}
}
