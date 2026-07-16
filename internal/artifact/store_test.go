package artifact

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opendatahub-io/mlflow-go/internal/transport"
)

func newTestStore(t *testing.T, handler http.Handler) *Store {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	tc, err := transport.New(transport.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("transport.New() error = %v", err)
	}

	return NewStore(tc)
}

func readDownload(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()
	t.Cleanup(func() { _ = rc.Close() })
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return data
}

func TestStore_UploadPresigned(t *testing.T) {
	presignedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "artifact-bytes" {
			t.Errorf("body = %q, want artifact-bytes", string(body))
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(presignedServer.Close)

	store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/presigned-upload-url"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"presigned_url": presignedServer.URL,
				"headers":       map[string]string{"Content-Type": "text/plain"},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))

	err := store.Upload(context.Background(), "run-1", "", "metrics.txt", []byte("artifact-bytes"), UploadOptions{ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
}

func TestStore_UploadProxyFallback(t *testing.T) {
	var uploadedPath string

	store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/presigned-upload-url"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{
				"error_code": "ENDPOINT_NOT_FOUND",
				"message":    "not supported",
			})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/mlflow-artifacts/artifacts/"):
			uploadedPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))

	artifactURI := "mlflow-artifacts:/experiments/1/runs/abc/artifacts"
	err := store.Upload(context.Background(), "run-1", artifactURI, "metrics.txt", []byte("proxy-bytes"), UploadOptions{})
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	wantPath := "/api/2.0/mlflow-artifacts/artifacts/experiments/1/runs/abc/artifacts/metrics.txt"
	if uploadedPath != wantPath {
		t.Errorf("upload path = %q, want %q", uploadedPath, wantPath)
	}
}

func TestStore_DownloadPresigned(t *testing.T) {
	presignedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("downloaded-bytes"))
	}))
	t.Cleanup(presignedServer.Close)

	store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/mlflow-artifacts/presigned/"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"url": presignedServer.URL,
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))

	artifactURI := "mlflow-artifacts:/experiments/1/runs/abc/artifacts"
	rc, err := store.Download(context.Background(), "run-1", artifactURI, "metrics.txt", DownloadOptions{})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	data := readDownload(t, rc)
	if string(data) != "downloaded-bytes" {
		t.Errorf("data = %q, want downloaded-bytes", string(data))
	}
}

func TestStore_DownloadCloudPresigned(t *testing.T) {
	presignedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("cloud-download"))
	}))
	t.Cleanup(presignedServer.Close)

	var presignedPath string
	store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/mlflow-artifacts/presigned/"):
			presignedPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"url": presignedServer.URL,
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))

	artifactURI := "s3://bucket/experiments/1/runs/abc/artifacts"
	rc, err := store.Download(context.Background(), "run-1", artifactURI, "metrics.txt", DownloadOptions{})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	data := readDownload(t, rc)
	if string(data) != "cloud-download" {
		t.Errorf("data = %q, want cloud-download", string(data))
	}
	wantPath := "/api/2.0/mlflow-artifacts/presigned/experiments/1/runs/abc/artifacts/metrics.txt"
	if presignedPath != wantPath {
		t.Errorf("presigned path = %q, want %q", presignedPath, wantPath)
	}
}

func TestStore_DownloadProxyFallback(t *testing.T) {
	store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/mlflow-artifacts/presigned/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{
				"error_code": "ENDPOINT_NOT_FOUND",
				"message":    "not supported",
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/mlflow-artifacts/artifacts/"):
			w.Write([]byte("proxy-download"))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))

	artifactURI := "mlflow-artifacts:/experiments/1/runs/abc/artifacts"
	rc, err := store.Download(context.Background(), "run-1", artifactURI, "metrics.txt", DownloadOptions{})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	data := readDownload(t, rc)
	if string(data) != "proxy-download" {
		t.Errorf("data = %q, want proxy-download", string(data))
	}
}

func TestStore_UploadTrackingServer(t *testing.T) {
	var uploaded bool

	store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/presigned-upload-url"):
			t.Errorf("tracking-server upload must not attempt presigned: %s %s", r.Method, r.URL.Path)
			http.Error(w, "presigned should be skipped", http.StatusInternalServerError)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/upload-artifact"):
			if r.URL.Query().Get("run_uuid") != "run-1" {
				t.Errorf("run_uuid = %q, want run-1", r.URL.Query().Get("run_uuid"))
			}
			if r.URL.Query().Get("path") != "metrics.txt" {
				t.Errorf("path = %q, want metrics.txt", r.URL.Query().Get("path"))
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != "tracking-server-bytes" {
				t.Errorf("body = %q, want tracking-server-bytes", string(body))
			}
			uploaded = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))

	artifactURI := "/tmp/mlruns/1/run-1/artifacts"
	err := store.Upload(context.Background(), "run-1", artifactURI, "metrics.txt", []byte("tracking-server-bytes"), UploadOptions{})
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if !uploaded {
		t.Error("expected artifact upload via tracking server")
	}
}

func TestStore_UploadTrackingServer_FileURI(t *testing.T) {
	var uploaded bool

	store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/presigned-upload-url"):
			t.Errorf("tracking-server upload must not attempt presigned: %s %s", r.Method, r.URL.Path)
			http.Error(w, "presigned should be skipped", http.StatusInternalServerError)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/upload-artifact"):
			uploaded = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))

	artifactURI := "file:///tmp/mlruns/1/run-1/artifacts"
	err := store.Upload(context.Background(), "run-1", artifactURI, "metrics.txt", []byte("file-uri-bytes"), UploadOptions{})
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if !uploaded {
		t.Error("expected artifact upload via tracking server")
	}
}

func TestStore_DownloadTrackingServer(t *testing.T) {
	store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/get-artifact":
			if r.URL.Query().Get("run_id") != "run-1" {
				t.Errorf("run_id = %q, want run-1", r.URL.Query().Get("run_id"))
			}
			if r.URL.Query().Get("path") != "metrics.txt" {
				t.Errorf("path = %q, want metrics.txt", r.URL.Query().Get("path"))
			}
			w.Write([]byte("tracking-server-download"))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))

	artifactURI := "file:///tmp/mlruns/1/run-1/artifacts"
	rc, err := store.Download(context.Background(), "run-1", artifactURI, "metrics.txt", DownloadOptions{})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	data := readDownload(t, rc)
	if string(data) != "tracking-server-download" {
		t.Errorf("data = %q, want tracking-server-download", string(data))
	}
}

func TestStore_ArtifactURI(t *testing.T) {
	store := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("run_id") != "run-1" {
			t.Errorf("run_id = %q, want run-1", r.URL.Query().Get("run_id"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"run": map[string]any{
				"info": map[string]any{
					"artifact_uri": "mlflow-artifacts:/experiments/1/runs/abc/artifacts",
				},
			},
		})
	}))

	artifactURI, err := store.ArtifactURI(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("ArtifactURI() error = %v", err)
	}
	if artifactURI != "mlflow-artifacts:/experiments/1/runs/abc/artifacts" {
		t.Errorf("ArtifactURI = %q, want mlflow-artifacts URI", artifactURI)
	}
}
