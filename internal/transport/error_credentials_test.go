package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	internalerrors "github.com/opendatahub-io/mlflow-go/internal/errors"
)

func TestErrorCookieCredentials(t *testing.T) {
	for _, source := range []string{"configured", "jar", "explicit"} {
		for _, structured := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/structured=%t", source, structured), func(t *testing.T) {
				t.Parallel()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if len(r.Cookies()) != 2 {
						t.Error("expected both authentication cookies")
					}
					w.WriteHeader(http.StatusConflict)
					message := "denied session-secret other-secret"
					if structured {
						_ = json.NewEncoder(w).Encode(map[string]string{"error_code": "session-secret", "message": message})
					} else {
						_ = json.NewEncoder(w).Encode(map[string]string{"detail": message})
					}
				}))
				defer server.Close()
				const cookie = `session=session-secret; other="other-secret"`
				cfg := Config{BaseURL: server.URL}
				headers := map[string]string{}
				switch source {
				case "configured":
					cfg.Headers = map[string]string{"cookie": cookie}
				case "explicit":
					headers["Cookie"] = cookie
				case "jar":
					jar, err := cookiejar.New(nil)
					if err != nil {
						t.Fatal(err)
					}
					u, _ := url.Parse(server.URL)
					jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "session-secret"}, {Name: "other", Value: "other-secret"}})
					cfg.HTTPClient = &http.Client{Jar: jar}
				}
				client, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if source == "explicit" {
					_, _, err = client.DoAbsolute(context.Background(), http.MethodGet, server.URL, headers, nil)
				} else {
					err = client.Get(context.Background(), "/", nil, nil)
				}
				var apiErr *internalerrors.APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("expected APIError, got %v", err)
				}
				if !strings.Contains(apiErr.Message, "denied [REDACTED] [REDACTED]") {
					t.Fatalf("missing sanitized diagnostics: %#v", apiErr)
				}
				for _, secret := range []string{"session-secret", "other-secret"} {
					if strings.Contains(apiErr.Code+apiErr.Message+err.Error(), secret) {
						t.Fatalf("cookie credential exposed: %#v", apiErr)
					}
				}
				if structured && internalerrors.IsAliasConflict(fmt.Errorf("caller: %w", err)) {
					t.Fatal("redacted cookie code must not classify an alias conflict")
				}
			})
		}
	}
}

func TestAbsoluteErrorQueryCredentials(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, structured := range []bool{false, true} {
			for _, redirect := range []bool{false, true} {
				t.Run(fmt.Sprintf("streaming=%t/structured=%t/redirect=%t", streaming, structured, redirect), func(t *testing.T) {
					t.Parallel()
					const secret = "query+secret/value= with space"
					query := "X-Amz-Security-Token=" + url.QueryEscape(secret)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/redirect" {
							http.Redirect(w, r, "/error?"+query, http.StatusFound)
							return
						}
						if r.URL.Query().Get("X-Amz-Security-Token") != secret {
							t.Error("missing query credential")
						}
						w.WriteHeader(http.StatusForbidden)
						message := "denied " + secret + " " + url.QueryEscape(secret) + " " + url.PathEscape(secret)
						if structured {
							_ = json.NewEncoder(w).Encode(map[string]string{"error_code": secret, "message": message})
						} else {
							_ = json.NewEncoder(w).Encode(map[string]string{"detail": message})
						}
					}))
					defer server.Close()
					// Artifact credentials belong to a foreign origin.
					client, err := New(Config{BaseURL: "https://tracking.example"})
					if err != nil {
						t.Fatal(err)
					}
					absoluteURL := server.URL + "/error?" + query
					if redirect {
						absoluteURL = server.URL + "/redirect"
					}
					if streaming {
						body, requestErr := client.DoAbsoluteGetBody(context.Background(), absoluteURL, nil)
						if body != nil {
							body.Close()
						}
						err = requestErr
					} else {
						_, _, err = client.DoAbsolute(context.Background(), http.MethodGet, absoluteURL, nil, nil)
					}
					var apiErr *internalerrors.APIError
					if !errors.As(err, &apiErr) {
						t.Fatalf("expected APIError, got %v", err)
					}
					if !strings.Contains(apiErr.Message, "denied [REDACTED] [REDACTED] [REDACTED]") {
						t.Fatalf("missing sanitized diagnostics: %#v", apiErr)
					}
					for _, value := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
						if strings.Contains(apiErr.Code+apiErr.Message+err.Error(), value) {
							t.Fatalf("query credential exposed: %#v", apiErr)
						}
					}
				})
			}
		}
	}
}

type enumeratingTransport struct {
	http.RoundTripper
	credentials []string
	safe        bool
}

func (t enumeratingTransport) ErrorRedactionCredentials(*http.Request) ([]string, bool) {
	return t.credentials, t.safe
}

func TestTransportCredentialContract(t *testing.T) {
	for _, safe := range []bool{false, true} {
		for _, code := range []string{"ALIAS_EXISTS", "RESOURCE_ALREADY_EXISTS", "CONTRACT_SECRET"} {
			t.Run(fmt.Sprintf("safe=%t/code=%s", safe, code), func(t *testing.T) {
				t.Parallel()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					status := http.StatusConflict
					if code == "RESOURCE_ALREADY_EXISTS" {
						status = http.StatusBadRequest
					}
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(map[string]string{"error_code": code, "message": "denied CONTRACT_SECRET"})
				}))
				defer server.Close()
				client, err := New(Config{BaseURL: server.URL, HTTPClient: &http.Client{Transport: enumeratingTransport{
					RoundTripper: http.DefaultTransport, credentials: []string{"CONTRACT_SECRET"}, safe: safe,
				}}})
				if err != nil {
					t.Fatal(err)
				}
				err = client.Get(context.Background(), "/", nil, nil)
				var apiErr *internalerrors.APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("expected APIError, got %v", err)
				}
				if strings.Contains(apiErr.Code+apiErr.Message+err.Error(), "CONTRACT_SECRET") {
					t.Fatalf("contract credential exposed: %#v", apiErr)
				}
				if safe && apiErr.Message != "denied [REDACTED]" || !safe && apiErr.Code != "" {
					t.Fatalf("unexpected sanitization: %#v", apiErr)
				}
				wrapped := fmt.Errorf("caller: %w", err)
				if got := internalerrors.IsAliasConflict(wrapped); got != (safe && code == "ALIAS_EXISTS") {
					t.Fatalf("IsAliasConflict = %t", got)
				}
				if code == "RESOURCE_ALREADY_EXISTS" && internalerrors.IsAlreadyExists(wrapped) != safe {
					t.Fatal("code-based recovery must require complete credential enumeration")
				}
			})
		}
	}
}
