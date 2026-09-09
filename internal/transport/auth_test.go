package transport

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFormatAuthHeader_BearerToken(t *testing.T) {
	got := FormatAuthHeader("my-secret-token")
	want := "Bearer my-secret-token"
	if got != want {
		t.Errorf("FormatAuthHeader() = %q, want %q", got, want)
	}
}

func TestFormatAuthHeader_BasicAuth(t *testing.T) {
	got := FormatAuthHeader("user:pass")
	if got[:6] != "Basic " {
		t.Errorf("FormatAuthHeader() = %q, want Basic prefix", got)
	}
}

func TestTokenRoundTripper(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: NewTokenRoundTripper(http.DefaultTransport, "my-token"),
	}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	resp.Body.Close()

	if receivedAuth != "Bearer my-token" {
		t.Errorf("Authorization = %q, want %q", receivedAuth, "Bearer my-token")
	}
}

func TestTokenFileRoundTripper(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("file-token\n"), 0600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: NewTokenFileRoundTripper(http.DefaultTransport, tokenFile),
	}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	resp.Body.Close()

	if receivedAuth != "Bearer file-token" {
		t.Errorf("Authorization = %q, want %q", receivedAuth, "Bearer file-token")
	}
}

func TestTokenFileRoundTripper_Rotation(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("token-v1"), 0600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: NewTokenFileRoundTripper(http.DefaultTransport, tokenFile),
	}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("first request error: %v", err)
	}
	resp.Body.Close()
	if receivedAuth != "Bearer token-v1" {
		t.Errorf("first request Authorization = %q, want %q", receivedAuth, "Bearer token-v1")
	}

	if err := os.WriteFile(tokenFile, []byte("token-v2"), 0600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	resp, err = client.Get(server.URL)
	if err != nil {
		t.Fatalf("second request error: %v", err)
	}
	resp.Body.Close()
	if receivedAuth != "Bearer token-v2" {
		t.Errorf("second request Authorization = %q, want %q", receivedAuth, "Bearer token-v2")
	}
}

func TestTokenFileRoundTripper_MissingFile(t *testing.T) {
	client := &http.Client{
		Transport: NewTokenFileRoundTripper(http.DefaultTransport, "/nonexistent/token"),
	}

	_, err := client.Get("http://localhost:1") //nolint:noctx // test helper
	if err == nil {
		t.Error("expected error for missing token file")
	}
}

func TestTokenFileRoundTripper_EmptyFile(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("  \n"), 0600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: NewTokenFileRoundTripper(http.DefaultTransport, tokenFile),
	}

	_, err := client.Get(server.URL)
	if err == nil {
		t.Error("expected error for empty token file")
	}
}
