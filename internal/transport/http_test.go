package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/opendatahub-io/mlflow-go/internal/errors"
)

func TestClient_Get_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Query().Get("name") != "test-prompt" {
			t.Errorf("expected query param name=test-prompt, got %s", r.URL.Query().Get("name"))
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected Authorization header, got %s", r.Header.Get("Authorization"))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL: server.URL,
		Headers: map[string]string{"Authorization": "Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var result map[string]string
	query := url.Values{"name": []string{"test-prompt"}}
	err = client.Get(context.Background(), "/api/test", query, &result)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if result["status"] != "ok" {
		t.Errorf("result = %v, want status=ok", result)
	}
}

func TestClient_Post_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}

		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "my-prompt" {
			t.Errorf("expected body.name=my-prompt, got %s", body["name"])
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"version": "1"})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var result map[string]string
	body := map[string]string{"name": "my-prompt"}
	err = client.Post(context.Background(), "/api/create", body, &result)
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}

	if result["version"] != "1" {
		t.Errorf("result = %v, want version=1", result)
	}
}

func TestClient_Patch_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}

		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["status"] != "active" {
			t.Errorf("expected body.status=active, got %s", body["status"])
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "active"})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var result map[string]string
	body := map[string]string{"status": "active"}
	err = client.Patch(context.Background(), "/api/update", body, &result)
	if err != nil {
		t.Fatalf("Patch() error = %v", err)
	}

	if result["status"] != "active" {
		t.Errorf("result = %v, want status=active", result)
	}
}

func TestClient_Patch_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error_code": "INVALID_PARAMETER_VALUE",
			"message":    "Invalid status",
		})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = client.Patch(context.Background(), "/api/update", map[string]string{"status": "bogus"}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	apiErr, ok := err.(*errors.APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, http.StatusBadRequest)
	}
}

func TestClient_Error_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{
			"error_code": "RESOURCE_DOES_NOT_EXIST",
			"message":    "Model not found",
		})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = client.Get(context.Background(), "/api/test", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.IsNotFound(err) {
		t.Errorf("expected IsNotFound, got %v", err)
	}

	apiErr, ok := err.(*errors.APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.Code != "RESOURCE_DOES_NOT_EXIST" {
		t.Errorf("Code = %q, want RESOURCE_DOES_NOT_EXIST", apiErr.Code)
	}
}

func TestClient_Error_BadRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error_code": "INVALID_PARAMETER_VALUE",
			"message":    "Invalid name",
		})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = client.Post(context.Background(), "/api/test", nil, nil)
	if !errors.IsInvalidArgument(err) {
		t.Errorf("expected IsInvalidArgument, got %v", err)
	}
}

func TestClient_Error_Conflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"error_code": "RESOURCE_ALREADY_EXISTS",
			"message":    "Model already exists",
		})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = client.Post(context.Background(), "/api/test", nil, nil)
	if !errors.IsAlreadyExists(err) {
		t.Errorf("expected IsAlreadyExists, got %v", err)
	}
}

func TestClient_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err = client.Get(ctx, "/api/test", nil, nil)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestNew_InvalidURL(t *testing.T) {
	_, err := New(Config{BaseURL: "://invalid"})
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestNew_DefaultTimeout(t *testing.T) {
	client, err := New(Config{BaseURL: "http://localhost"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if client.httpClient.Timeout != 30*time.Second {
		t.Errorf("timeout = %v, want 30s", client.httpClient.Timeout)
	}
}

func TestNew_CustomTimeout(t *testing.T) {
	client, err := New(Config{
		BaseURL: "http://localhost",
		Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if client.httpClient.Timeout != 60*time.Second {
		t.Errorf("timeout = %v, want 60s", client.httpClient.Timeout)
	}
}

func TestNew_StreamClientHasNoOverallTimeout(t *testing.T) {
	// A token installs an auth round-tripper, giving both clients a non-nil
	// Transport so the sharing check below is meaningful.
	client, err := New(Config{BaseURL: "https://localhost", Timeout: 60 * time.Second, Token: "tok"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if client.httpClient.Timeout != 60*time.Second {
		t.Errorf("httpClient.Timeout = %v, want 60s", client.httpClient.Timeout)
	}
	if client.streamClient.Timeout != 0 {
		t.Errorf("streamClient.Timeout = %v, want 0", client.streamClient.Timeout)
	}
	// The stream client must share the API client's Transport so it keeps the
	// connection pool, auth, workspace and connection-level timeouts.
	if client.streamClient.Transport != client.httpClient.Transport {
		t.Error("streamClient must share httpClient's Transport")
	}
}

func TestClient_TimeoutExceeded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL: server.URL,
		Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = client.Get(context.Background(), "/api/test", nil, nil)
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestClient_NoAuthHeader_WhenNoToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("expected no Authorization header, got %s", auth)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = client.Get(context.Background(), "/api/test", nil, nil)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
}

// testLogHandler captures log records for testing.
type testLogHandler struct {
	records []testLogRecord
}

type testLogRecord struct {
	Level   string
	Message string
	Attrs   map[string]any
}

func (h *testLogHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (h *testLogHandler) Handle(_ context.Context, r slog.Record) error {
	record := testLogRecord{
		Level:   r.Level.String(),
		Message: r.Message,
		Attrs:   make(map[string]any),
	}
	r.Attrs(func(a slog.Attr) bool {
		record.Attrs[a.Key] = a.Value.Any()
		return true
	})
	h.records = append(h.records, record)
	return nil
}

func (h *testLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *testLogHandler) WithGroup(name string) slog.Handler {
	return h
}

func TestClient_LogsRequestAndResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "ok"}`))
	}))
	defer server.Close()

	handler := &testLogHandler{}
	logger := slog.New(handler)

	client, err := New(Config{
		BaseURL: server.URL,
		Headers: map[string]string{"Authorization": "Bearer secret-token"},
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var result map[string]string
	err = client.Get(context.Background(), "/api/test", nil, &result)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	// Should have 2 log records: request and response
	if len(handler.records) != 2 {
		t.Fatalf("expected 2 log records, got %d", len(handler.records))
	}

	// Check request log
	reqLog := handler.records[0]
	if reqLog.Message != "request" {
		t.Errorf("request log message = %q, want %q", reqLog.Message, "request")
	}
	if reqLog.Attrs["method"] != "GET" {
		t.Errorf("request log method = %v, want GET", reqLog.Attrs["method"])
	}
	if reqLog.Attrs["url"] == nil {
		t.Error("request log should have url")
	}

	// Check response log
	respLog := handler.records[1]
	if respLog.Message != "response" {
		t.Errorf("response log message = %q, want %q", respLog.Message, "response")
	}
	if respLog.Attrs["status"] != int64(200) {
		t.Errorf("response log status = %v, want 200", respLog.Attrs["status"])
	}
	if respLog.Attrs["duration_ms"] == nil {
		t.Error("response log should have duration_ms")
	}
}

func TestClient_NoLogsWithoutLogger(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// No logger provided
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// This should not panic or fail even without a logger
	err = client.Get(context.Background(), "/api/test", nil, nil)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
}

func TestClient_LogsNeverIncludeSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"secret": "should-not-be-logged"}`))
	}))
	defer server.Close()

	handler := &testLogHandler{}
	logger := slog.New(handler)

	client, err := New(Config{
		BaseURL: server.URL,
		Headers: map[string]string{"Authorization": "Bearer super-secret-token"},
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	body := map[string]string{"password": "secret123", "template": "Hello {{name}}"}
	err = client.Post(context.Background(), "/api/test", body, nil)
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}

	// Verify no secrets in logs
	for _, record := range handler.records {
		for key, val := range record.Attrs {
			strVal, ok := val.(string)
			if !ok {
				continue
			}
			// Check that sensitive data is not logged
			if key == "token" || key == "password" || key == "secret" {
				t.Errorf("sensitive key %q should not be logged", key)
			}
			if strVal == "super-secret-token" || strVal == "secret123" {
				t.Errorf("sensitive value should not be logged: %s=%s", key, strVal)
			}
			// Body content should not be logged
			if strVal == "Hello {{name}}" {
				t.Errorf("request body content should not be logged")
			}
			if strVal == "should-not-be-logged" {
				t.Errorf("response body content should not be logged")
			}
		}
	}
}

func TestCustomHeadersSentOnRequest(t *testing.T) {
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL: server.URL,
		Headers: map[string]string{
			"X-MLFLOW-WORKSPACE": "team-bella",
			"X-Custom":           "value-123",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = client.Get(context.Background(), "/test", nil, nil)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if got := receivedHeaders.Get("X-MLFLOW-WORKSPACE"); got != "team-bella" {
		t.Errorf("X-MLFLOW-WORKSPACE = %q, want %q", got, "team-bella")
	}
	if got := receivedHeaders.Get("X-Custom"); got != "value-123" {
		t.Errorf("X-Custom = %q, want %q", got, "value-123")
	}
	// Standard headers should still be present
	if got := receivedHeaders.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
}

func TestCustomHeadersWithToken(t *testing.T) {
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL: server.URL,
		Headers: map[string]string{
			"Authorization":      "Bearer my-token",
			"X-MLFLOW-WORKSPACE": "team-dora",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = client.Get(context.Background(), "/test", nil, nil)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if got := receivedHeaders.Get("Authorization"); got != "Bearer my-token" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer my-token")
	}
	if got := receivedHeaders.Get("X-MLFLOW-WORKSPACE"); got != "team-dora" {
		t.Errorf("X-MLFLOW-WORKSPACE = %q, want %q", got, "team-dora")
	}
}

func TestClient_GetBytes_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.Header.Get("Accept") == "application/json" {
			t.Error("GetBytes should not set Accept: application/json")
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("artifact-data"))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	data, contentType, err := client.GetBytes(context.Background(), "/api/artifacts/file", nil)
	if err != nil {
		t.Fatalf("GetBytes() error = %v", err)
	}
	if string(data) != "artifact-data" {
		t.Errorf("data = %q, want artifact-data", string(data))
	}
	if contentType != "application/octet-stream" {
		t.Errorf("contentType = %q, want application/octet-stream", contentType)
	}
}

func TestClient_GetBodyStream_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Write([]byte("stream-data"))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	rc, err := client.GetBodyStream(context.Background(), "/api/artifacts/file", nil)
	if err != nil {
		t.Fatalf("GetBodyStream() error = %v", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(data) != "stream-data" {
		t.Errorf("data = %q, want stream-data", string(data))
	}
}

// TestClient_GetBodyStream_ExceedsMaxResponseBodySize verifies that the
// streaming download path is not subject to the maxResponseBodySize cap that
// buffered API responses enforce, so arbitrarily large artifacts can be read in
// full.
func TestClient_GetBodyStream_ExceedsMaxResponseBodySize(t *testing.T) {
	const bodySize = maxResponseBodySize + 1
	chunk := bytes.Repeat([]byte("a"), 1<<20) // 1 MiB

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remaining := int64(bodySize)
		for remaining > 0 {
			n := int64(len(chunk))
			if n > remaining {
				n = remaining
			}
			if _, err := w.Write(chunk[:n]); err != nil {
				return
			}
			remaining -= n
		}
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	rc, err := client.GetBodyStream(context.Background(), "/api/artifacts/big", nil)
	if err != nil {
		t.Fatalf("GetBodyStream() error = %v", err)
	}
	defer rc.Close()

	n, err := io.Copy(io.Discard, rc)
	if err != nil {
		t.Fatalf("io.Copy() error = %v, want nil (stream must not be capped)", err)
	}
	if n != bodySize {
		t.Errorf("read %d bytes, want %d", n, int64(bodySize))
	}
}

// TestClient_GetBodyStream_NotBoundByOverallTimeout verifies that a streaming
// download is not aborted by the overall http.Client.Timeout, so a slow transfer
// that outlasts the configured timeout still completes.
func TestClient_GetBodyStream_NotBoundByOverallTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter is not a Flusher")
		}
		w.Write([]byte("start"))
		fl.Flush()
		time.Sleep(250 * time.Millisecond) // outlasts the 50ms overall Timeout
		w.Write([]byte("-end"))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	rc, err := client.GetBodyStream(context.Background(), "/api/artifacts/slow", nil)
	if err != nil {
		t.Fatalf("GetBodyStream() error = %v", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll() error = %v, want nil (stream must not be bound by overall timeout)", err)
	}
	if string(data) != "start-end" {
		t.Errorf("data = %q, want start-end", string(data))
	}
}

// TestClient_GetBodyStream_HeaderPhaseBounded verifies that a streaming download
// does not block forever when the server accepts the connection but never sends
// response headers: with a context that has no deadline, the response-header
// phase is bounded by the dedicated StreamHeaderTimeout (independent of the API
// Timeout).
func TestClient_GetBodyStream_HeaderPhaseBounded(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the request open without sending headers
	}))
	defer server.Close()
	defer close(release)

	// A long API Timeout must not shorten the header phase; only
	// StreamHeaderTimeout governs it.
	client, err := New(Config{BaseURL: server.URL, Timeout: time.Minute, StreamHeaderTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, gerr := client.GetBodyStream(context.Background(), "/api/artifacts/no-headers", nil)
		done <- gerr
	}()

	select {
	case gerr := <-done:
		if gerr == nil {
			t.Fatal("expected error when headers do not arrive within the deadline")
		}
		// The timer cancels the request context, so Do fails with
		// context.Canceled; the error must be reported as a header timeout, not a
		// bare cancellation, so the caller can tell it apart from its own.
		if !strings.Contains(gerr.Error(), "response headers not received") {
			t.Errorf("error = %v, want response-headers-not-received message", gerr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GetBodyStream blocked past the header deadline")
	}
}

// ctxIgnoringRoundTripper returns a successful response after a delay, ignoring
// request-context cancellation. It simulates the race where the header deadline
// fires just before Do returns success.
type ctxIgnoringRoundTripper struct {
	delay time.Duration
	body  string
}

func (rt ctxIgnoringRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	time.Sleep(rt.delay)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(rt.body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// TestClient_GetBodyStream_HeaderTimerFiresAfterSuccess covers the race where the
// header deadline fires (cancelling the request context) just before Do returns
// success. The returned body would be tied to a cancelled context, so its first
// read would fail; GetBodyStream must instead report the header timeout.
func TestClient_GetBodyStream_HeaderTimerFiresAfterSuccess(t *testing.T) {
	hc := &http.Client{
		Transport: ctxIgnoringRoundTripper{delay: 80 * time.Millisecond, body: "data"},
	}
	client, err := New(Config{
		BaseURL:             "http://example.invalid",
		HTTPClient:          hc,
		StreamHeaderTimeout: 20 * time.Millisecond, // header deadline
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	rc, err := client.GetBodyStream(context.Background(), "/api/artifacts/file", nil)
	if err == nil {
		rc.Close()
		t.Fatal("expected error when the header deadline fired despite a successful response")
	}
	if !strings.Contains(err.Error(), "response headers not received") {
		t.Errorf("error = %v, want response-headers-not-received message", err)
	}
}

// TestClient_GetBodyStream_ContextDeadlineGovernsHeaderPhase verifies that when
// the caller's context already carries a deadline, no separate StreamHeaderTimeout
// timer is imposed: a download whose headers arrive after StreamHeaderTimeout but
// before the context deadline still succeeds. This is the MLflow proxy case, where
// a large proxied artifact can take longer than a fixed header timeout to reach
// first byte and the caller's context is the intended bound.
func TestClient_GetBodyStream_ContextDeadlineGovernsHeaderPhase(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond) // outlasts StreamHeaderTimeout below
		w.Write([]byte("artifact"))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, StreamHeaderTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rc, err := client.GetBodyStream(ctx, "/api/artifacts/slow-headers", nil)
	if err != nil {
		t.Fatalf("GetBodyStream() error = %v, want nil (context deadline, not StreamHeaderTimeout, must govern)", err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != "artifact" {
		t.Errorf("body = %q, want artifact", string(body))
	}
}

// TestNew_StreamHeaderTimeout verifies how Config.StreamHeaderTimeout resolves:
// zero uses the generous default, a positive value is kept, and a negative value
// disables the fallback (rely solely on the request context).
func TestNew_StreamHeaderTimeout(t *testing.T) {
	tests := []struct {
		name string
		cfg  time.Duration
		want time.Duration
	}{
		{"default", 0, defaultStreamHeaderTimeout},
		{"explicit", 90 * time.Second, 90 * time.Second},
		{"disabled", -1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := New(Config{BaseURL: "https://localhost", StreamHeaderTimeout: tt.cfg})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if client.streamHeaderTimeout != tt.want {
				t.Errorf("streamHeaderTimeout = %v, want %v", client.streamHeaderTimeout, tt.want)
			}
		})
	}
}

// TestClient_PutReader_NotBoundByOverallTimeout verifies that a streaming upload
// is not aborted by the overall http.Client.Timeout.
func TestClient_PutReader_NotBoundByOverallTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		time.Sleep(250 * time.Millisecond) // outlasts the 50ms overall Timeout
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = client.PutReader(context.Background(), "/api/artifacts/slow", strings.NewReader("data"), "text/plain")
	if err != nil {
		t.Fatalf("PutReader() error = %v, want nil (upload must not be bound by overall timeout)", err)
	}
}

func TestClient_PutBytes_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("Content-Type = %q, want application/octet-stream", ct)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "upload-data" {
			t.Errorf("body = %q, want upload-data", string(body))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = client.PutBytes(context.Background(), "/api/artifacts/file", []byte("upload-data"), "application/octet-stream")
	if err != nil {
		t.Fatalf("PutBytes() error = %v", err)
	}
}

func TestClient_PutReader_StreamsBody(t *testing.T) {
	var receivedBody string
	var receivedCT string
	var contentLength int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		receivedCT = r.Header.Get("Content-Type")
		contentLength = r.ContentLength
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// A pipe reader has no known length, so the request must stream via chunked
	// transfer encoding (ContentLength == -1 server-side).
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("streamed-upload"))
		pw.Close()
	}()

	err = client.PutReader(context.Background(), "/api/artifacts/file", pr, "application/octet-stream")
	if err != nil {
		t.Fatalf("PutReader() error = %v", err)
	}
	if receivedBody != "streamed-upload" {
		t.Errorf("body = %q, want streamed-upload", receivedBody)
	}
	if receivedCT != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", receivedCT)
	}
	if contentLength != -1 {
		t.Errorf("ContentLength = %d, want -1 (chunked stream)", contentLength)
	}
}

// TestClient_PutReader_RedirectIsError verifies that a 3xx response is treated
// as a failure even when a Location header is present. Go would otherwise follow
// a 301/302/303 as a bodyless GET; if that GET returns 200 the upload would be
// reported as success while the server stored nothing.
func TestClient_PutReader_RedirectIsError(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		location string
	}{
		{"307 no location", http.StatusTemporaryRedirect, ""},
		{"302 with location", http.StatusFound, "/api/artifacts/moved"},
		{"301 with location", http.StatusMovedPermanently, "/api/artifacts/moved"},
		{"303 with location", http.StatusSeeOther, "/api/artifacts/moved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var redirectTarget string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/artifacts/moved" {
					// The (wrongly) followed GET target: succeeds and stores nothing.
					redirectTarget = r.Method
					w.WriteHeader(http.StatusOK)
					return
				}
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()

			client, err := New(Config{BaseURL: server.URL})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			err = client.PutReader(context.Background(), "/api/artifacts/file", strings.NewReader("data"), "text/plain")
			if err == nil {
				t.Fatal("expected error for 3xx redirect response, got nil")
			}
			if redirectTarget != "" {
				t.Errorf("redirect was followed (as %s); it must not be", redirectTarget)
			}
		})
	}
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (t *trackingReadCloser) Close() error {
	t.closed = true
	return nil
}

// TestClient_PutReader_DoesNotCloseCallerBody verifies PutReader honors its
// body-ownership contract: net/http's Transport closes the request body, so an
// io.Closer body (e.g. an *os.File) must be wrapped to keep it open for the
// caller.
func TestClient_PutReader_DoesNotCloseCallerBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	body := &trackingReadCloser{Reader: strings.NewReader("data")}
	if err := client.PutReader(context.Background(), "/api/artifacts/file", body, "text/plain"); err != nil {
		t.Fatalf("PutReader() error = %v", err)
	}
	if body.closed {
		t.Error("PutReader closed the caller's body; it must retain ownership")
	}
}

// TestClient_PutReader_KnownLengthSetsContentLength verifies that a non-closer
// body whose length is known (e.g. *strings.Reader) is not wrapped, so
// http.NewRequest still sets Content-Length instead of using chunked encoding.
func TestClient_PutReader_KnownLengthSetsContentLength(t *testing.T) {
	var contentLength int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentLength = r.ContentLength
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	const payload = "hello"
	if err := client.PutReader(context.Background(), "/api/artifacts/file", strings.NewReader(payload), "text/plain"); err != nil {
		t.Fatalf("PutReader() error = %v", err)
	}
	if contentLength != int64(len(payload)) {
		t.Errorf("ContentLength = %d, want %d", contentLength, len(payload))
	}
}

func TestClient_PutReader_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = client.PutReader(context.Background(), "/api/artifacts/file", strings.NewReader("data"), "text/plain")
	if err == nil {
		t.Fatal("expected error from server 500")
	}
}

// TestClient_PutReader_HeaderPhaseBounded verifies that an upload does not block
// forever when the server consumes the whole body and then never sends response
// headers: with a context that has no deadline, the wait for headers is bounded by
// StreamHeaderTimeout once the body has been sent.
func TestClient_PutReader_HeaderPhaseBounded(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // drain the upload, then stall without responding
		<-release
	}))
	defer server.Close()
	defer close(release)

	client, err := New(Config{BaseURL: server.URL, StreamHeaderTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- client.PutReader(context.Background(), "/api/artifacts/no-headers", strings.NewReader("data"), "text/plain")
	}()

	select {
	case perr := <-done:
		if perr == nil {
			t.Fatal("expected error when response headers do not arrive within the deadline")
		}
		if !strings.Contains(perr.Error(), "response headers not received") {
			t.Errorf("error = %v, want response-headers-not-received message", perr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PutReader blocked past the header deadline")
	}
}

// TestClient_PutReader_WorkspaceProbeBounded verifies that the workspace probe,
// which runs before the upload request is written (and so is not covered by the
// WroteRequest-triggered header timer), is itself bounded: a server that never
// answers /server-info must not hang an upload that has no context deadline.
func TestClient_PutReader_WorkspaceProbeBounded(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, serverInfoPath) {
			<-release // stall the probe without ever answering
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(release)

	client, err := New(Config{
		BaseURL:             server.URL,
		Workspace:           "team-x",
		WorkspacesSupport:   true,
		StreamHeaderTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- client.PutReader(context.Background(), "/api/artifacts/file", strings.NewReader("data"), "text/plain")
	}()

	select {
	case perr := <-done:
		if perr == nil {
			t.Fatal("expected error when the workspace probe never answers")
		}
		if !strings.Contains(perr.Error(), "workspace probe failed") {
			t.Errorf("error = %v, want workspace-probe-failed message", perr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PutReader blocked on an unbounded workspace probe")
	}
}

// slowReader emits its data one chunk per Read with a delay before each, so the
// full body takes len(chunks)*delay to send.
type slowReader struct {
	chunks [][]byte
	delay  time.Duration
	i      int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.i >= len(s.chunks) {
		return 0, io.EOF
	}
	time.Sleep(s.delay)
	n := copy(p, s.chunks[s.i])
	s.i++
	return n, nil
}

// TestClient_PutReader_SlowBodyNotBoundByHeaderTimeout verifies that the header
// timeout does not cap the transfer itself: a body that takes longer than
// StreamHeaderTimeout to send still succeeds, because the timer starts only after
// the request is fully written and the server responds promptly thereafter.
func TestClient_PutReader_SlowBodyNotBoundByHeaderTimeout(t *testing.T) {
	var received int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		received = int(n)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, StreamHeaderTimeout: 80 * time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Sending takes ~4*60ms = 240ms, well past the 80ms header timeout, but the
	// timer must not start until the body is fully sent.
	body := &slowReader{
		chunks: [][]byte{[]byte("aa"), []byte("bb"), []byte("cc"), []byte("dd")},
		delay:  60 * time.Millisecond,
	}
	err = client.PutReader(context.Background(), "/api/artifacts/slow-body", body, "text/plain")
	if err != nil {
		t.Fatalf("PutReader() error = %v, want nil (header timeout must not cap the transfer)", err)
	}
	if received != 8 {
		t.Errorf("server received %d bytes, want 8", received)
	}
}

func TestClient_PostBytes_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Query().Get("run_uuid") != "run-1" {
			t.Errorf("run_uuid = %q, want run-1", r.URL.Query().Get("run_uuid"))
		}
		if ct := r.Header.Get("Content-Type"); ct != "text/plain" {
			t.Errorf("Content-Type = %q, want text/plain", ct)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "post-data" {
			t.Errorf("body = %q, want post-data", string(body))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	query := url.Values{"run_uuid": []string{"run-1"}}
	err = client.PostBytes(context.Background(), "/upload", query, []byte("post-data"), "text/plain")
	if err != nil {
		t.Fatalf("PostBytes() error = %v", err)
	}
}

func TestClient_GetBytes_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{
			"error_code": "RESOURCE_DOES_NOT_EXIST",
			"message":    "Artifact not found",
		})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, _, err = client.GetBytes(context.Background(), "/api/artifacts/missing", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.IsNotFound(err) {
		t.Errorf("expected IsNotFound, got %v", err)
	}
}

func TestClient_DoAbsolute_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.Header.Get("X-Amz-Signature") != "abc123" {
			t.Errorf("expected X-Amz-Signature header")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "presigned-upload" {
			t.Errorf("body = %q, want presigned-upload", string(body))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: "http://localhost:9999"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	data, _, err := client.DoAbsolute(
		context.Background(),
		http.MethodPut,
		server.URL+"/presigned",
		map[string]string{"X-Amz-Signature": "abc123", "Content-Type": "application/octet-stream"},
		[]byte("presigned-upload"),
	)
	if err != nil {
		t.Fatalf("DoAbsolute() error = %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty response body, got %q", string(data))
	}
}

func TestReadResponseBody_ExceedsLimit(t *testing.T) {
	_, err := readResponseBody(strings.NewReader(strings.Repeat("a", maxResponseBodySize+1)))
	if err == nil {
		t.Fatal("expected error for oversized body, got nil")
	}
}

func TestRedactAbsoluteURLForLog(t *testing.T) {
	got := redactAbsoluteURLForLog("https://storage.example.com/object?X-Amz-Signature=secret&X-Amz-Credential=abc")
	want := "https://storage.example.com/object"
	if got != want {
		t.Errorf("redactAbsoluteURLForLog() = %q, want %q", got, want)
	}
}

func TestClient_DoAbsoluteGetBody_RedactsPresignedURLInLogs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery == "" {
			t.Error("request must keep presigned query params")
		}
		w.Write([]byte("artifact-bytes"))
	}))
	defer server.Close()

	handler := &testLogHandler{}
	logger := slog.New(handler)

	client, err := New(Config{
		BaseURL: "http://localhost:9999",
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	presignedURL := server.URL + "/object?X-Amz-Signature=secret&X-Amz-Credential=abc"
	rc, err := client.DoAbsoluteGetBody(context.Background(), presignedURL, nil)
	if err != nil {
		t.Fatalf("DoAbsoluteGetBody() error = %v", err)
	}
	defer rc.Close()

	if _, err := io.ReadAll(rc); err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}

	var requestLogs int
	for _, record := range handler.records {
		if record.Message != "request" {
			continue
		}
		requestLogs++
		urlAttr, _ := record.Attrs["url"].(string)
		if strings.Contains(urlAttr, "X-Amz-Signature") || strings.Contains(urlAttr, "secret") {
			t.Errorf("request log url leaked presigned query params: %q", urlAttr)
		}
		if !strings.HasSuffix(urlAttr, "/object") {
			t.Errorf("request log url = %q, want host/path without query", urlAttr)
		}
	}
	if requestLogs != 1 {
		t.Fatalf("expected 1 request log, got %d", requestLogs)
	}
}

func TestNew_RejectsInsecureWithToken(t *testing.T) {
	_, err := New(Config{
		BaseURL:  "https://mlflow.example.com",
		Insecure: true,
		Token:    "secret",
	})
	if err == nil {
		t.Fatal("expected error when Insecure and Token are both set")
	}
	if !strings.Contains(err.Error(), "insecure") {
		t.Errorf("error = %v, want mention of insecure", err)
	}
}

func TestNew_RejectsInsecureWithTokenPath(t *testing.T) {
	_, err := New(Config{
		BaseURL:   "https://mlflow.example.com",
		Insecure:  true,
		TokenPath: "/var/run/secrets/token",
	})
	if err == nil {
		t.Fatal("expected error when Insecure and TokenPath are both set")
	}
}

func TestNew_AllowsInsecureWithoutToken(t *testing.T) {
	_, err := New(Config{
		BaseURL:  "https://mlflow.example.com",
		Insecure: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v; Insecure without token should be allowed", err)
	}
}
