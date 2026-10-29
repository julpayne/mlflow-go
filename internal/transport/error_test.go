package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	internalerrors "github.com/opendatahub-io/mlflow-go/internal/errors"
)

func TestHTTPErrorBodies(t *testing.T) {
	tests := []struct {
		name, body, code, message string
	}{
		{"standard MLflow", `{"error_code":"PERMISSION_DENIED","message":"Access denied"}`, "PERMISSION_DENIED", "Access denied"},
		{"nonstandard JSON", `{"detail":"Namespace creation is not supported by this provider"}`, "", `Forbidden: response body "{\"detail\":\"Namespace creation is not supported by this provider\"}"`},
		{"plain text", "Namespace creation is not supported by this provider", "", `Forbidden: response body "Namespace creation is not supported by this provider"`},
		{"empty", "", "", "Forbidden: empty response body"},
		{"whitespace", " \n\t ", "", "Forbidden: empty response body"},
		{"malformed JSON", `{"message":`, "", `Forbidden: response body "{\"message\":"`},
		{"empty object", `{}`, "", `Forbidden: response body "{}"`},
		{"null", `null`, "", `Forbidden: response body "null"`},
		{"array", `["denied"]`, "", `Forbidden: response body "[\"denied\"]"`},
		{"empty MLflow fields", `{"error_code":"","message":""}`, "", `Forbidden: response body "{\"error_code\":\"\",\"message\":\"\"}"`},
		{"message only", `{"message":"Access denied"}`, "", "Access denied"},
		{"invalid code type", `{"error_code":403,"message":"Access denied"}`, "", "Access denied"},
		{"invalid message type", `{"error_code":"PERMISSION_DENIED","message":{"detail":"denied"}}`, "PERMISSION_DENIED", `Forbidden: response body "{\"error_code\":\"PERMISSION_DENIED\",\"message\":{\"detail\":\"denied\"}}"`},
		{"code only", `{"error_code":"PERMISSION_DENIED"}`, "PERMISSION_DENIED", `Forbidden: response body "{\"error_code\":\"PERMISSION_DENIED\"}"`},
		{"control characters", "denied\n\x1b[31m", "", `Forbidden: response body "denied\n\x1b[31m"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// Intentionally advertise JSON even for non-JSON bodies: decoding
				// must cope with the actual bytes, not trust Content-Type.
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Set-Cookie", "session=response-secret")
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, Headers: map[string]string{
				"Authorization": "Bearer request-secret", "X-Api-Key": "api-key-secret",
			}})
			if err != nil {
				t.Fatal(err)
			}
			err = client.Get(context.Background(), "/error", nil, nil)
			var apiErr *internalerrors.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("expected APIError, got %v", err)
			}
			if apiErr.StatusCode != http.StatusForbidden || apiErr.Code != tt.code || apiErr.Message != tt.message {
				t.Fatalf("APIError = %#v, want status 403, code %q, message %q", apiErr, tt.code, tt.message)
			}
			wrapped := fmt.Errorf("caller: %w", err)
			if !errors.Is(wrapped, apiErr) || !internalerrors.IsPermissionDenied(wrapped) || internalerrors.IsAlreadyExists(wrapped) || internalerrors.IsNotFound(wrapped) {
				t.Errorf("error identity or classification changed: %v", wrapped)
			}
			for _, secret := range []string{"request-secret", "api-key-secret", "response-secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error exposes header secret %q", secret)
				}
			}
		})
	}
}

func TestErrorBodyRedaction(t *testing.T) {
	origin, err := url.Parse("https://tracking.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		headers map[string]string
		rt      http.RoundTripper
		body    string
		want    string
	}{
		{"configured header", map[string]string{"X-API-Key": "header-secret"}, nil, "denied header-secret", `Forbidden: response body "denied [REDACTED]"`},
		{"bearer header", map[string]string{"Authorization": "Bearer bearer-secret"}, nil, "denied bearer-secret", `Forbidden: response body "denied [REDACTED]"`},
		{"static bearer", nil, NewTokenRoundTripper(http.DefaultTransport, "token-secret", origin), "denied token-secret", `Forbidden: response body "denied [REDACTED]"`},
		{"static basic", nil, NewTokenRoundTripper(http.DefaultTransport, "alice:password-secret", origin), "denied alice:password-secret " + base64.StdEncoding.EncodeToString([]byte("alice:password-secret")) + " password-secret", `Forbidden: response body "denied [REDACTED] [REDACTED] [REDACTED]"`},
		{"rotating token", nil, NewTokenFileRoundTripper(http.DefaultTransport, "unused-token-file", origin), "denied old-token", "Forbidden"},
		{"custom transport", nil, roundTripperFunc(func(*http.Request) (*http.Response, error) { panic("unused") }), "denied custom-secret", "Forbidden"},
		{"JSON escaped header", map[string]string{"X-Key": "secret\"value"}, nil, `{"detail":"secret\"value"}`, `Forbidden: response body "{\"detail\":\"[REDACTED]\"}"`},
		{"secret crossing limit", map[string]string{"X-Key": "boundary-secret"}, nil, strings.Repeat("x", 4090) + "boundary-secret", "Forbidden: response body " + fmt.Sprintf("%q", strings.Repeat("x", 4090)+"[REDAC") + " (truncated)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{headers: tt.headers, httpClient: &http.Client{Transport: tt.rt}}
			var apiErr *internalerrors.APIError
			if err := client.parseError(http.StatusForbidden, []byte(tt.body), nil); !errors.As(err, &apiErr) {
				t.Fatalf("expected APIError, got %v", err)
			}
			if apiErr.Message != tt.want {
				t.Fatalf("Message = %q, want %q", apiErr.Message, tt.want)
			}
		})
	}
}

func TestStructuredErrorRedaction(t *testing.T) {
	origin, err := url.Parse("https://tracking.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, code, message, wantCode, wantMessage string
		headers                                    map[string]string
		rt                                         http.RoundTripper
		request                                    *http.Request
	}{
		{name: "ordinary fields", code: "RESOURCE_NOT_FOUND", message: "  missing resource\n", wantCode: "RESOURCE_NOT_FOUND", wantMessage: "  missing resource\n"},
		{name: "configured header in both fields", headers: map[string]string{"X-Key": "header-secret"}, code: "header-secret", message: "denied header-secret", wantCode: "[REDACTED]", wantMessage: "denied [REDACTED]"},
		{name: "escaped header", headers: map[string]string{"X-Key": "secret\"value"}, code: "PERMISSION_DENIED", message: "denied secret\"value", wantCode: "PERMISSION_DENIED", wantMessage: "denied [REDACTED]"},
		{name: "static bearer", rt: NewTokenRoundTripper(http.DefaultTransport, "bearer-secret", origin), code: "bearer-secret", message: "denied Bearer bearer-secret", wantCode: "[REDACTED]", wantMessage: "denied [REDACTED]"},
		{name: "basic components", rt: NewTokenRoundTripper(http.DefaultTransport, "alice:password-secret", origin), code: "PERMISSION_DENIED", message: "denied alice:password-secret password-secret", wantCode: "PERMISSION_DENIED", wantMessage: "denied [REDACTED] [REDACTED]"},
		{name: "explicit request authorization", request: &http.Request{Header: http.Header{"Authorization": {"Bearer caller-secret"}}}, code: "caller-secret", message: "denied caller-secret", wantCode: "[REDACTED]", wantMessage: "denied [REDACTED]"},
		{name: "token file without request", rt: NewTokenFileRoundTripper(http.DefaultTransport, "unused", origin), code: "unknown-secret", message: "denied unknown-secret", wantMessage: "Forbidden"},
		{name: "opaque transport", rt: roundTripperFunc(func(*http.Request) (*http.Response, error) { panic("unused") }), code: "unknown-secret", message: "denied unknown-secret", wantMessage: "Forbidden"},
		{name: "code with empty message", headers: map[string]string{"X-Key": "header-secret"}, code: "header-secret", wantCode: "[REDACTED]", wantMessage: `Forbidden: response body "{\"error_code\":\"[REDACTED]\",\"message\":\"\"}"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]string{"error_code": tt.code, "message": tt.message})
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{headers: tt.headers, httpClient: &http.Client{Transport: tt.rt}}
			var apiErr *internalerrors.APIError
			if err := client.parseError(http.StatusForbidden, body, tt.request); !errors.As(err, &apiErr) {
				t.Fatalf("expected APIError, got %v", err)
			}
			if apiErr.Code != tt.wantCode || apiErr.Message != tt.wantMessage || apiErr.StatusCode != http.StatusForbidden {
				t.Fatalf("APIError = %#v, want code %q, message %q, status 403", apiErr, tt.wantCode, tt.wantMessage)
			}
		})
	}
}

func TestErrorBodyExcerptBound(t *testing.T) {
	client := &Client{}
	// The final multi-byte character straddles the byte limit.
	err := client.parseError(http.StatusForbidden, []byte(strings.Repeat("x", 4095)+strings.Repeat("界", 10000)), nil)
	if len(err.Error()) > 4200 || !strings.Contains(err.Error(), "(truncated)") || !utf8.ValidString(err.Error()) {
		t.Fatalf("fallback must be bounded, marked truncated, and valid UTF-8: length %d", len(err.Error()))
	}
	// Quoting can expand each byte by up to four characters, but stays bounded.
	err = client.parseError(599, []byte(strings.Repeat("\x00", 10000)), nil)
	if len(err.Error()) > 16500 || !strings.Contains(err.Error(), "HTTP request failed") {
		t.Fatalf("unexpected fallback for unknown status: length %d", len(err.Error()))
	}
}

func TestErrorBodyProductionTransports(t *testing.T) {
	const secret = "production-secret"
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == serverInfoPath {
			fmt.Fprint(w, `{"workspaces_enabled":true}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, "denied %s", r.Header.Get("Authorization"))
	}))
	defer server.Close()
	custom := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+secret)
		return http.DefaultTransport.RoundTrip(r)
	})
	for _, tt := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"default", Config{}, `Forbidden: response body "denied"`},
		{"insecure", Config{Insecure: true}, `Forbidden: response body "denied"`},
		{"nil client transport", Config{HTTPClient: &http.Client{}}, `Forbidden: response body "denied"`},
		{"explicit HTTP transport", Config{HTTPClient: server.Client()}, `Forbidden: response body "denied"`},
		{"workspace", Config{Workspace: "test"}, `Forbidden: response body "denied"`},
		{"static token", Config{Token: secret}, `Forbidden: response body "denied [REDACTED]"`},
		{"workspace and static token", Config{Workspace: "test", WorkspacesSupport: true, Token: secret}, `Forbidden: response body "denied [REDACTED]"`},
		{"token file", Config{TokenPath: tokenPath}, `Forbidden: response body "denied [REDACTED]"`},
		{"workspace and token file", Config{Workspace: "test", TokenPath: tokenPath}, `Forbidden: response body "denied [REDACTED]"`},
		{"custom transport", Config{HTTPClient: &http.Client{Transport: custom}}, "Forbidden"},
		{"workspace and custom transport", Config{Workspace: "test", Token: secret, HTTPClient: &http.Client{Transport: custom}}, "Forbidden"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			cfg.BaseURL = server.URL
			client, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var apiErr *internalerrors.APIError
			if err := client.Get(context.Background(), "/error", nil, nil); !errors.As(err, &apiErr) {
				t.Fatalf("expected APIError, got %v", err)
			}
			if apiErr.Message != tt.want {
				t.Fatalf("Message = %q, want %q", apiErr.Message, tt.want)
			}
		})
	}
}

func TestErrorBodyTokenFileRotation(t *testing.T) {
	const secret = "request-token-secret"
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Rotate before parsing the response so rereading the file would miss
		// the credential reflected by this request.
		if err := os.WriteFile(tokenPath, []byte("rotated-token"), 0600); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, "denied %s", r.Header.Get("Authorization"))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, TokenPath: tokenPath})
	if err != nil {
		t.Fatal(err)
	}
	var apiErr *internalerrors.APIError
	if err := client.Get(context.Background(), "/error", nil, nil); !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if want := `Forbidden: response body "denied [REDACTED]"`; apiErr.Message != want {
		t.Fatalf("Message = %q, want %q", apiErr.Message, want)
	}
}

func TestBoundedRedactedExcerpt(t *testing.T) {
	for _, tt := range []struct {
		name, body, secret string
	}{
		{"expanding replacements", strings.Repeat("s", 10000), "s"},
		{"contracting replacements", strings.Repeat(strings.Repeat("s", 100), 10000), strings.Repeat("s", 100)},
		{"crossing limit", strings.Repeat("x", 4090) + "secret", "secret"},
		{"leading whitespace in secret", " secret", " secret"},
		{"trailing whitespace in secret", "secret ", "secret "},
		{"only whitespace", strings.Repeat("\u2003", 10000), "secret"},
		{"long trailing whitespace", "denied" + strings.Repeat(" ", 10000), "secret"},
		{"long trailing Unicode whitespace", "denied" + strings.Repeat("\u2003", 10000), "secret"},
		{"whitespace before later text", "denied" + strings.Repeat(" ", 10000) + "end", "secret"},
		{"Unicode boundary", strings.Repeat("界", 10000), "secret"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Compare bounded processing with the full-body reference behavior.
			want := strings.TrimSpace(strings.ReplaceAll(tt.body, tt.secret, "[REDACTED]"))
			truncated := len(want) > 4096
			if truncated {
				want = want[:4096]
			}
			got, gotTruncated := boundedRedactedExcerpt([]byte(tt.body), []string{tt.secret})
			if got != want || gotTruncated != truncated {
				t.Fatalf("excerpt mismatch: length %d, truncated %v; want length %d, truncated %v", len(got), gotTruncated, len(want), truncated)
			}
		})
	}
}

func TestStructuredErrorTokenFileRotation(t *testing.T) {
	const secret = "original-request-token"
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := os.WriteFile(tokenPath, []byte("rotated-token"), 0600); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error_code": strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
			"message":    "denied " + r.Header.Get("Authorization"),
		})
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, TokenPath: tokenPath})
	if err != nil {
		t.Fatal(err)
	}
	var apiErr *internalerrors.APIError
	err = client.Get(context.Background(), "/error", nil, nil)
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if apiErr.Code != "[REDACTED]" || apiErr.Message != "denied [REDACTED]" || strings.Contains(err.Error(), secret) {
		t.Fatalf("unexpected error: %#v", apiErr)
	}
}

func TestAbsoluteErrorRequestHeaderRedaction(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, structured := range []bool{false, true} {
			t.Run(fmt.Sprintf("streaming=%t/structured=%t", streaming, structured), func(t *testing.T) {
				t.Parallel()
				const apiKey = "per-request-api-key"
				const storageToken = "per-request-storage-token"
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-API-Key") != apiKey || r.Header.Get("X-Amz-Security-Token") != storageToken {
						t.Error("missing per-request credentials")
					}
					message := "denied " + r.Header.Get("X-API-Key") + " " + r.Header.Get("X-Amz-Security-Token")
					w.WriteHeader(http.StatusForbidden)
					if structured {
						_ = json.NewEncoder(w).Encode(map[string]string{"error_code": r.Header.Get("X-API-Key"), "message": message})
					} else {
						fmt.Fprint(w, message)
					}
				}))
				defer server.Close()
				// Artifact requests use a different origin from the tracking server.
				client, err := New(Config{BaseURL: "https://tracking.example"})
				if err != nil {
					t.Fatal(err)
				}
				headers := map[string]string{"X-API-Key": apiKey, "X-Amz-Security-Token": storageToken}
				if streaming {
					body, requestErr := client.DoAbsoluteGetBody(context.Background(), server.URL, headers)
					if body != nil {
						body.Close()
					}
					err = requestErr
				} else {
					_, _, err = client.DoAbsolute(context.Background(), http.MethodGet, server.URL, headers, nil)
				}
				var apiErr *internalerrors.APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("expected APIError, got %v", err)
				}
				wantCode, wantMessage := "", `Forbidden: response body "denied [REDACTED] [REDACTED]"`
				if structured {
					wantCode, wantMessage = "[REDACTED]", "denied [REDACTED] [REDACTED]"
				}
				if apiErr.StatusCode != http.StatusForbidden || apiErr.Code != wantCode || apiErr.Message != wantMessage {
					t.Fatalf("APIError = %#v, want status 403, code %q, message %q", apiErr, wantCode, wantMessage)
				}
				for _, secret := range []string{apiKey, storageToken} {
					if strings.Contains(err.Error(), secret) {
						t.Errorf("error exposes per-request credential %q", secret)
					}
				}
			})
		}
	}
}

func TestErrorRequestMetadataPreserved(t *testing.T) {
	for _, name := range []string{
		"Accept", "Accept-Charset", "Accept-Encoding", "Accept-Language",
		"Content-Type", "Content-Length", "Content-Encoding", "Content-Language",
		workspaceHeader,
	} {
		t.Run(name, func(t *testing.T) {
			// Use lowercase keys too: callers may construct Header directly.
			request := &http.Request{Header: http.Header{strings.ToLower(name): {"a"}}}
			client := &Client{headers: map[string]string{strings.ToLower(name): "a"}}
			for _, body := range []string{
				`{"error_code":"ALIAS_CONFLICT","message":"alias application/json"}`,
				"alias application/json",
			} {
				apiErr := client.parseError(http.StatusConflict, []byte(body), request).(*internalerrors.APIError)
				if !strings.Contains(apiErr.Message, "alias application/json") || strings.Contains(apiErr.Message, "[REDACTED]") {
					t.Fatalf("request metadata redacted ordinary text: %#v", apiErr)
				}
				if strings.HasPrefix(body, "{") && !internalerrors.IsAliasConflict(fmt.Errorf("caller: %w", apiErr)) {
					t.Fatalf("known transport lost alias-conflict classification: %#v", apiErr)
				}
			}
		})
	}
}

func TestOpaqueTransportConflictClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		// A credential could itself look like a valid MLflow conflict code.
		fmt.Fprint(w, `{"error_code":"ALIAS_CONFLICT","message":"resource has aliases"}`)
	}))
	defer server.Close()
	client, err := New(Config{
		BaseURL: server.URL,
		HTTPClient: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return http.DefaultTransport.RoundTrip(r)
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Get(context.Background(), "/error", nil, nil)
	var apiErr *internalerrors.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if apiErr.StatusCode != http.StatusConflict || apiErr.Code != "" || apiErr.Message != "Conflict" {
		t.Fatalf("unexpected redacted APIError: %#v", apiErr)
	}
	wrapped := fmt.Errorf("caller: %w", err)
	if internalerrors.IsAliasConflict(wrapped) {
		t.Error("redacted conflict must not be classified as an alias conflict")
	}
	if !internalerrors.IsAlreadyExists(wrapped) {
		t.Error("redaction must preserve status-based already-exists classification")
	}
}
