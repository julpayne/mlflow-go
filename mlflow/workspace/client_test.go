package workspace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/opendatahub-io/mlflow-go/internal/errors"
	"github.com/opendatahub-io/mlflow-go/internal/transport"
)

// isLive reports whether MLFLOW_TRACKING_URI is set, meaning tests should
// run against a real MLflow service.
func isLive() bool { return os.Getenv("MLFLOW_TRACKING_URI") != "" }

// skipIfLive skips the current test when a live service is configured.
func skipIfLive(t *testing.T, reason string) {
	t.Helper()
	if isLive() {
		t.Skipf("mock-only: %s", reason)
	}
}

// newTestClient returns a workspace Client. When MLFLOW_TRACKING_URI is set
// the mock handler is ignored and the client targets the live service;
// otherwise an httptest.Server is started with the given handler.
func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()

	baseURL := os.Getenv("MLFLOW_TRACKING_URI")
	if baseURL == "" {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		baseURL = server.URL
	}

	tc, err := transport.New(transport.Config{BaseURL: baseURL})
	if err != nil {
		t.Fatalf("transport.New() error = %v", err)
	}

	return NewClient(tc)
}

func mustEncodeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("failed to encode response: %v", err)
	}
}

func workspaceJSON(name string) map[string]any {
	return map[string]any{"workspace": map[string]any{"name": name}}
}

// ---------------------------------------------------------------------------
// GetServerInfo
// ---------------------------------------------------------------------------

func TestGetServerInfo_Enabled(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/3.0/mlflow/server-info" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mustEncodeJSON(t, w, map[string]any{"workspaces_enabled": true})
	}))

	info, err := client.GetServerInfo(context.Background())
	if err != nil {
		t.Fatalf("GetServerInfo() error = %v", err)
	}
	if isLive() {
		t.Logf("live server: WorkspacesEnabled = %v", info.WorkspacesEnabled)
	} else if !info.WorkspacesEnabled {
		t.Error("expected WorkspacesEnabled = true")
	}
}

func TestGetServerInfo_Disabled(t *testing.T) {
	skipIfLive(t, "tests a fabricated server response")

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mustEncodeJSON(t, w, map[string]any{"workspaces_enabled": false})
	}))

	info, err := client.GetServerInfo(context.Background())
	if err != nil {
		t.Fatalf("GetServerInfo() error = %v", err)
	}
	if info.WorkspacesEnabled {
		t.Error("expected WorkspacesEnabled = false")
	}
}

// ---------------------------------------------------------------------------
// GetWorkspace
// ---------------------------------------------------------------------------

func TestGetWorkspace_Success(t *testing.T) {
	name := "my-ws"
	if isLive() {
		name = "default"
	}

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mustEncodeJSON(t, w, workspaceJSON(name))
	}))

	ws, err := client.GetWorkspace(context.Background(), name)
	if err != nil {
		t.Fatalf("GetWorkspace(%q) error = %v", name, err)
	}
	if ws.Name != name {
		t.Errorf("Name = %q, want %q", ws.Name, name)
	}
}

func TestGetWorkspace_EmptyName(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if _, err := client.GetWorkspace(context.Background(), ""); err == nil {
		t.Error("expected error for empty name")
	}
}

// ---------------------------------------------------------------------------
// CreateWorkspace
// ---------------------------------------------------------------------------

func TestCreateWorkspace_Success(t *testing.T) {
	skipIfLive(t, "avoid side effects on live service")

	var receivedName string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req map[string]string
		json.NewDecoder(r.Body).Decode(&req)
		receivedName = req["name"]
		mustEncodeJSON(t, w, workspaceJSON(req["name"]))
	}))

	ws, err := client.CreateWorkspace(context.Background(), "new-ws")
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	if ws.Name != "new-ws" {
		t.Errorf("Name = %q, want %q", ws.Name, "new-ws")
	}
	if receivedName != "new-ws" {
		t.Errorf("received name = %q, want %q", receivedName, "new-ws")
	}
}

func TestCreateWorkspace_EmptyName(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if _, err := client.CreateWorkspace(context.Background(), ""); err == nil {
		t.Error("expected error for empty name")
	}
}

// ---------------------------------------------------------------------------
// EnsureWorkspace
// ---------------------------------------------------------------------------

func TestEnsureWorkspace_CreatesNew(t *testing.T) {
	skipIfLive(t, "tests create-path routing")

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/3.0/mlflow/workspaces" {
			mustEncodeJSON(t, w, workspaceJSON("new-ws"))
			return
		}
		http.NotFound(w, r)
	}))

	ws, err := client.EnsureWorkspace(context.Background(), "new-ws")
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if ws.Name != "new-ws" {
		t.Errorf("Name = %q, want %q", ws.Name, "new-ws")
	}
}

func TestEnsureWorkspace_AlreadyExists(t *testing.T) {
	skipIfLive(t, "tests create→get fallback with RESOURCE_ALREADY_EXISTS")

	calls := 0
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		calls++
		if r.Method == http.MethodPost && r.URL.Path == "/api/3.0/mlflow/workspaces" {
			w.WriteHeader(http.StatusConflict)
			mustEncodeJSON(t, w, map[string]string{
				"error_code": "RESOURCE_ALREADY_EXISTS",
				"message":    "workspace exists",
			})
			return
		}
		mustEncodeJSON(t, w, workspaceJSON("existing-ws"))
	}))

	ws, err := client.EnsureWorkspace(context.Background(), "existing-ws")
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if ws.Name != "existing-ws" {
		t.Errorf("Name = %q, want %q", ws.Name, "existing-ws")
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (create + get), got %d", calls)
	}
}

func TestEnsureWorkspace_Default(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Errorf("expected GET for default workspace, got %s", r.Method)
		}
		mustEncodeJSON(t, w, workspaceJSON("default"))
	}))

	ws, err := client.EnsureWorkspace(context.Background(), "default")
	if err != nil {
		t.Fatalf("EnsureWorkspace(default) error = %v", err)
	}
	if ws.Name != "default" {
		t.Errorf("Name = %q, want %q", ws.Name, "default")
	}
}

func TestEnsureWorkspace_EmptyName(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if _, err := client.EnsureWorkspace(context.Background(), ""); err == nil {
		t.Error("expected error for empty name")
	}
}

func TestEnsureWorkspace_CreateFails(t *testing.T) {
	skipIfLive(t, "tests server-error handling")

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		mustEncodeJSON(t, w, map[string]string{
			"error_code": "INTERNAL_ERROR",
			"message":    "server failure",
		})
	}))

	if _, err := client.EnsureWorkspace(context.Background(), "fail-ws"); err == nil {
		t.Error("expected error for server failure")
	}
}

// Ensure errors import is used by referencing it.
var _ = errors.IsAlreadyExists
