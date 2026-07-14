package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestClient_ListArtifacts(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("run_id") != "run-1" {
			t.Errorf("run_id = %q, want run-1", r.URL.Query().Get("run_id"))
		}
		if r.URL.Query().Get("path") != "models" {
			t.Errorf("path = %q, want models", r.URL.Query().Get("path"))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"root_uri": "mlflow-artifacts:/experiments/1/runs/abc/artifacts",
			"files": []map[string]any{
				{"path": "models/model.pkl", "is_dir": false, "file_size": 42},
			},
		})
	}))

	result, err := client.ListArtifacts(context.Background(), "run-1", WithArtifactPath("models"))
	if err != nil {
		t.Fatalf("ListArtifacts() error = %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("len(Files) = %d, want 1", len(result.Files))
	}
	if result.Files[0].Path != "models/model.pkl" {
		t.Errorf("Files[0].Path = %q, want models/model.pkl", result.Files[0].Path)
	}
	if result.Files[0].FileSize != 42 {
		t.Errorf("Files[0].FileSize = %d, want 42", result.Files[0].FileSize)
	}
}

func TestClient_LogArtifactProxy(t *testing.T) {
	var uploaded bool

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/runs/get"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"run": map[string]any{
					"info": map[string]any{
						"artifact_uri": "mlflow-artifacts:/experiments/1/runs/abc/artifacts",
					},
				},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/presigned-upload-url"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{
				"error_code": "ENDPOINT_NOT_FOUND",
				"message":    "not supported",
			})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/mlflow-artifacts/artifacts/"):
			body, _ := io.ReadAll(r.Body)
			if string(body) != "hello" {
				t.Errorf("upload body = %q, want hello", string(body))
			}
			uploaded = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))

	err := client.LogArtifact(context.Background(), "run-1", "greeting.txt", bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("LogArtifact() error = %v", err)
	}
	if !uploaded {
		t.Error("expected artifact upload via proxy")
	}
}

func TestClient_DownloadArtifactProxy(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/runs/get"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"run": map[string]any{
					"info": map[string]any{
						"artifact_uri": "mlflow-artifacts:/experiments/1/runs/abc/artifacts",
					},
				},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/mlflow-artifacts/presigned/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{
				"error_code": "ENDPOINT_NOT_FOUND",
				"message":    "not supported",
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/mlflow-artifacts/artifacts/"):
			w.Write([]byte("artifact-content"))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))

	rc, err := client.DownloadArtifact(context.Background(), "run-1", "greeting.txt")
	if err != nil {
		t.Fatalf("DownloadArtifact() error = %v", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != "artifact-content" {
		t.Errorf("data = %q, want artifact-content", string(body))
	}
}

func TestClient_ListArtifacts_Validation(t *testing.T) {
	var unexpectedRequests []string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unexpectedRequests = append(unexpectedRequests, r.Method+" "+r.URL.Path)
		http.NotFound(w, r)
	}))

	_, err := client.ListArtifacts(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty run ID")
	}
	if len(unexpectedRequests) > 0 {
		t.Errorf("unexpected HTTP requests: %v", unexpectedRequests)
	}
}

func TestClient_LogArtifact_Validation(t *testing.T) {
	var unexpectedRequests []string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unexpectedRequests = append(unexpectedRequests, r.Method+" "+r.URL.Path)
		http.NotFound(w, r)
	}))

	err := client.LogArtifact(context.Background(), "", "file.txt", bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("expected error for empty run ID")
	}

	err = client.LogArtifact(context.Background(), "run-1", "", bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("expected error for empty artifact path")
	}

	err = client.LogArtifact(context.Background(), "run-1", "file.txt", nil)
	if err == nil {
		t.Fatal("expected error for nil reader")
	}

	if len(unexpectedRequests) > 0 {
		t.Errorf("unexpected HTTP requests: %v", unexpectedRequests)
	}
}

type byteCountReader struct {
	remaining int64
}

func (r *byteCountReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	r.remaining -= n
	return int(n), nil
}

func TestReadArtifactContent_ExceedsMaxSize(t *testing.T) {
	_, err := readArtifactContent(&byteCountReader{remaining: maxArtifactUploadSize + 1})
	if err == nil {
		t.Fatal("expected error for oversized artifact content")
	}
	if !strings.Contains(err.Error(), "maximum upload size") {
		t.Errorf("error = %v, want maximum upload size message", err)
	}
}
