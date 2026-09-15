package workspace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opendatahub-io/mlflow-go/internal/errors"
	"github.com/opendatahub-io/mlflow-go/internal/transport"
)

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	tc, err := transport.New(transport.Config{BaseURL: server.URL})
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
	if !info.WorkspacesEnabled {
		t.Error("expected WorkspacesEnabled = true")
	}
}

func TestGetServerInfo_Disabled(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestGetWorkspace_Success(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mustEncodeJSON(t, w, map[string]any{
			"workspace": map[string]any{
				"name":          "my-ws",
				"creation_time": 1700000000000,
			},
		})
	}))

	ws, err := client.GetWorkspace(context.Background(), "my-ws")
	if err != nil {
		t.Fatalf("GetWorkspace() error = %v", err)
	}
	if ws.Name != "my-ws" {
		t.Errorf("Name = %q, want %q", ws.Name, "my-ws")
	}
	if ws.CreationTime.IsZero() {
		t.Error("CreationTime should not be zero")
	}
}

func TestGetWorkspace_EmptyName(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	_, err := client.GetWorkspace(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestCreateWorkspace_Success(t *testing.T) {
	var receivedName string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req map[string]string
		json.NewDecoder(r.Body).Decode(&req)
		receivedName = req["name"]
		mustEncodeJSON(t, w, map[string]any{
			"workspace": map[string]any{
				"name":          req["name"],
				"creation_time": 1700000000000,
			},
		})
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
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	_, err := client.CreateWorkspace(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestEnsureWorkspace_CreatesNew(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/3.0/mlflow/workspaces/create" {
			mustEncodeJSON(t, w, map[string]any{
				"workspace": map[string]any{
					"name":          "new-ws",
					"creation_time": 1700000000000,
				},
			})
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
	calls := 0
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		calls++
		if r.URL.Path == "/api/3.0/mlflow/workspaces/create" {
			w.WriteHeader(http.StatusConflict)
			mustEncodeJSON(t, w, map[string]string{
				"error_code": "RESOURCE_ALREADY_EXISTS",
				"message":    "workspace exists",
			})
			return
		}
		mustEncodeJSON(t, w, map[string]any{
			"workspace": map[string]any{
				"name":          "existing-ws",
				"creation_time": 1700000000000,
			},
		})
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
		mustEncodeJSON(t, w, map[string]any{
			"workspace": map[string]any{
				"name":          "default",
				"creation_time": 1700000000000,
			},
		})
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
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	_, err := client.EnsureWorkspace(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestEnsureWorkspace_CreateFails(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		mustEncodeJSON(t, w, map[string]string{
			"error_code": "INTERNAL_ERROR",
			"message":    "server failure",
		})
	}))

	_, err := client.EnsureWorkspace(context.Background(), "fail-ws")
	if err == nil {
		t.Error("expected error for server failure")
	}
}

// Ensure errors import is used by referencing it.
var _ = errors.IsAlreadyExists
