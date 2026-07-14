package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/opendatahub-io/mlflow-go/internal/errors"
)

const maxResponseBodySize = 100 << 20 // 100 MiB

// Client handles HTTP communication with the MLflow API.
type Client struct {
	baseURL    *url.URL
	headers    map[string]string
	httpClient *http.Client
	logger     *slog.Logger
}

// Config holds configuration for creating a transport Client.
type Config struct {
	BaseURL    string
	Headers    map[string]string
	HTTPClient *http.Client
	Logger     *slog.Logger
	Timeout    time.Duration
	Insecure   bool
}

// errorResponse represents the MLflow API error format.
type errorResponse struct {
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
}

// New creates a new transport Client.
func New(cfg Config) (*Client, error) {
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		timeout := cfg.Timeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		httpClient = &http.Client{Timeout: timeout}
		if cfg.Insecure {
			if dt, ok := http.DefaultTransport.(*http.Transport); ok {
				tr := dt.Clone()
				tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}} //nolint:gosec // user-requested via WithInsecure
				httpClient.Transport = tr
			} else {
				httpClient.Transport = &http.Transport{
					ForceAttemptHTTP2: true,
					TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}, //nolint:gosec // user-requested via WithInsecure
				}
			}
		}
	}

	return &Client{
		baseURL:    baseURL,
		headers:    cfg.Headers,
		httpClient: httpClient,
		logger:     cfg.Logger,
	}, nil
}

// Get performs a GET request to the specified path with query parameters.
func (c *Client) Get(ctx context.Context, path string, query url.Values, result any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, result)
}

// Post performs a POST request to the specified path with a JSON body.
func (c *Client) Post(ctx context.Context, path string, body, result any) error {
	return c.do(ctx, http.MethodPost, path, nil, body, result)
}

// Delete performs a DELETE request to the specified path with a JSON body.
func (c *Client) Delete(ctx context.Context, path string, body, result any) error {
	return c.do(ctx, http.MethodDelete, path, nil, body, result)
}

// GetBytes performs a GET request and returns the raw response body.
func (c *Client) GetBytes(ctx context.Context, path string, query url.Values) ([]byte, string, error) {
	return c.doRaw(ctx, http.MethodGet, path, query, nil, "", false)
}

// PutBytes performs a PUT request with a raw body and content type.
func (c *Client) PutBytes(ctx context.Context, path string, body []byte, contentType string) error {
	_, _, err := c.doRaw(ctx, http.MethodPut, path, nil, body, contentType, false)
	return err
}

// PostBytes performs a POST request with a raw body and content type.
func (c *Client) PostBytes(ctx context.Context, path string, query url.Values, body []byte, contentType string) error {
	_, _, err := c.doRaw(ctx, http.MethodPost, path, query, body, contentType, false)
	return err
}

// DoAbsolute performs an HTTP request to an absolute URL outside the tracking server base URL.
// This is used for presigned artifact upload/download URLs.
func (c *Client) DoAbsolute(ctx context.Context, method, absoluteURL string, headers map[string]string, body []byte) ([]byte, string, error) {
	return c.doAbsolute(ctx, method, absoluteURL, headers, body)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, result any) error {
	// Build request URL, preserving any path prefix from the base URL
	// (e.g., base "https://host/mlflow" + path "/api/2.0/mlflow/..." → "/mlflow/api/2.0/mlflow/...")
	fullPath := strings.TrimRight(c.baseURL.Path, "/") + path
	reqURL := c.baseURL.ResolveReference(&url.URL{Path: fullPath, RawQuery: query.Encode()})

	// Encode body if present
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to encode request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	// Log request
	start := time.Now()
	if c.logger != nil {
		c.logger.Debug("request",
			"method", method,
			"url", reqURL.String(),
		)
	}

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Log response
	duration := time.Since(start)
	if c.logger != nil {
		c.logger.Debug("response",
			"status", resp.StatusCode,
			"duration_ms", duration.Milliseconds(),
		)
	}

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Handle error responses
	if resp.StatusCode >= 400 {
		return c.parseError(resp.StatusCode, respBody)
	}

	// Decode successful response
	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}

func (c *Client) doRaw(ctx context.Context, method, path string, query url.Values, body []byte, contentType string, jsonAccept bool) ([]byte, string, error) {
	fullPath := strings.TrimRight(c.baseURL.Path, "/") + path
	reqURL := c.baseURL.ResolveReference(&url.URL{Path: fullPath, RawQuery: query.Encode()})

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), bodyReader)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %w", err)
	}

	if body != nil && contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if jsonAccept {
		req.Header.Set("Accept", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	if c.logger != nil {
		c.logger.Debug("request",
			"method", method,
			"url", reqURL.String(),
		)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if c.logger != nil {
		c.logger.Debug("response",
			"status", resp.StatusCode,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}

	respBody, err := readResponseBody(resp.Body)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode >= 400 {
		return nil, "", c.parseError(resp.StatusCode, respBody)
	}

	return respBody, resp.Header.Get("Content-Type"), nil
}

func (c *Client) doAbsolute(ctx context.Context, method, absoluteURL string, headers map[string]string, body []byte) ([]byte, string, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, absoluteURL, bodyReader)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	if c.logger != nil {
		c.logger.Debug("request",
			"method", method,
			"url", redactAbsoluteURLForLog(absoluteURL),
		)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if c.logger != nil {
		c.logger.Debug("response",
			"status", resp.StatusCode,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}

	respBody, err := readResponseBody(resp.Body)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode >= 400 {
		return nil, "", c.parseError(resp.StatusCode, respBody)
	}

	return respBody, resp.Header.Get("Content-Type"), nil
}

func readResponseBody(r io.Reader) ([]byte, error) {
	limited := io.LimitReader(r, maxResponseBodySize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if int64(len(data)) > maxResponseBodySize {
		return nil, fmt.Errorf("response body exceeds maximum size of %d bytes", maxResponseBodySize)
	}
	return data, nil
}

func redactAbsoluteURLForLog(absoluteURL string) string {
	parsed, err := url.Parse(absoluteURL)
	if err != nil {
		return absoluteURL
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func (c *Client) parseError(statusCode int, body []byte) error {
	var errResp errorResponse
	if err := json.Unmarshal(body, &errResp); err != nil {
		// If we can't parse the error, return a generic one
		return &errors.APIError{
			StatusCode: statusCode,
			Message:    string(body),
		}
	}

	return &errors.APIError{
		StatusCode: statusCode,
		Code:       errResp.ErrorCode,
		Message:    errResp.Message,
	}
}
