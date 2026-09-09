package transport

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}

func TestTokenRoundTripper(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: NewTokenRoundTripper(http.DefaultTransport, "my-token", mustParseURL(t, server.URL)),
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
		Transport: NewTokenFileRoundTripper(http.DefaultTransport, tokenFile, mustParseURL(t, server.URL)),
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
		Transport: NewTokenFileRoundTripper(http.DefaultTransport, tokenFile, mustParseURL(t, server.URL)),
	}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("first request error: %v", err)
	}
	resp.Body.Close()
	if receivedAuth != "Bearer token-v1" {
		t.Errorf("first request Authorization = %q, want %q", receivedAuth, "Bearer token-v1")
	}

	err = os.WriteFile(tokenFile, []byte("token-v2"), 0600)
	if err != nil {
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
	target := mustParseURL(t, "http://localhost:1")
	client := &http.Client{
		Transport: NewTokenFileRoundTripper(http.DefaultTransport, "/nonexistent/token", target),
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
		Transport: NewTokenFileRoundTripper(http.DefaultTransport, tokenFile, mustParseURL(t, server.URL)),
	}

	_, err := client.Get(server.URL)
	if err == nil {
		t.Error("expected error for empty token file")
	}
}

func TestTokenRoundTripper_NoAuthOnRedirectToForeignHost(t *testing.T) {
	var foreignAuth string
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer foreign.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/callback", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	client := &http.Client{
		Transport: NewTokenRoundTripper(http.DefaultTransport, "secret", mustParseURL(t, origin.URL)),
	}

	resp, err := client.Get(origin.URL + "/start")
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	resp.Body.Close()

	if foreignAuth != "" {
		t.Errorf("foreign host received Authorization = %q, want empty", foreignAuth)
	}
}

func TestTokenFileRoundTripper_NoAuthOnRedirectToForeignHost(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("secret"), 0600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	var foreignAuth string
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer foreign.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/callback", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	client := &http.Client{
		Transport: NewTokenFileRoundTripper(http.DefaultTransport, tokenFile, mustParseURL(t, origin.URL)),
	}

	resp, err := client.Get(origin.URL + "/start")
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	resp.Body.Close()

	if foreignAuth != "" {
		t.Errorf("foreign host received Authorization = %q, want empty", foreignAuth)
	}
}
