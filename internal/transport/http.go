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
	"sync"
	"time"

	"github.com/opendatahub-io/mlflow-go/internal/errors"
)

const maxResponseBodySize = 100 << 20 // 100 MiB

// defaultStreamHeaderTimeout is the fallback deadline for the response-header
// phase of a streaming transfer when the request context carries no deadline. It
// is deliberately generous: MLflow's mlflow-artifacts proxy fetches the whole
// remote object from the backing store before it sends any response header, so a
// large artifact can take well over the 30s API timeout to reach first byte. It
// only guards against a server that accepts the connection and never responds.
const defaultStreamHeaderTimeout = 5 * time.Minute

// Client handles HTTP communication with the MLflow API.
type Client struct {
	baseURL    *url.URL
	headers    map[string]string
	httpClient *http.Client // API calls; overall http.Client.Timeout applies
	// streamClient handles artifact uploads/downloads. It shares httpClient's
	// Transport (connection pool, dial and TLS handshake timeouts, auth and
	// workspace round-trippers) but drops the overall http.Client.Timeout, which
	// covers the entire exchange including body transfer and would otherwise abort
	// an arbitrarily large streaming upload/download mid-transfer. Streaming
	// transfers are bounded by the request context instead; the response-header
	// phase is separately bounded (the shared Transport may set no
	// ResponseHeaderTimeout) — for downloads in doRequestBody, and for uploads in
	// PutReader once the request body has been fully sent.
	streamClient *http.Client
	// streamHeaderTimeout bounds the response-header phase of a streaming transfer
	// (download, or upload once the request body has been sent) only when the
	// request context has no deadline. Zero disables the fallback (rely solely on
	// the context). It is independent of the API Timeout so a slow proxied
	// transfer is not cut off before the object is exchanged.
	streamHeaderTimeout time.Duration
	logger              *slog.Logger
}

// Config holds configuration for creating a transport Client.
type Config struct {
	BaseURL           string
	Headers           map[string]string
	HTTPClient        *http.Client
	Logger            *slog.Logger
	Timeout           time.Duration
	Insecure          bool
	Token             string
	TokenPath         string
	Workspace         string
	WorkspacesSupport bool
	// StreamHeaderTimeout bounds the response-header phase of a streaming
	// artifact transfer (download or upload) when the request context has no
	// deadline; for uploads the timer starts only after the request body has been
	// sent. It is independent of Timeout so a slow proxied transfer is not aborted
	// before the object is exchanged. Zero uses defaultStreamHeaderTimeout; a
	// negative value disables the fallback entirely (rely solely on the context).
	// Callers handling very large artifacts should supply a context deadline or a
	// negative value.
	StreamHeaderTimeout time.Duration
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

	hasAuth := cfg.Token != "" || cfg.TokenPath != ""
	if cfg.Insecure && hasAuth {
		return nil, fmt.Errorf("refusing to send credentials over an insecure connection " +
			"(plain HTTP or TLS verification disabled); remove WithInsecure or the token/token-path option")
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

	wrapped := wrapClientWithWorkspace(wrapClientWithAuth(httpClient, cfg.Token, cfg.TokenPath, baseURL), cfg.Workspace, cfg.BaseURL, cfg.WorkspacesSupport)

	// Derive the streaming client from wrapped so it shares the same Transport
	// (and thus connection pool, dial/TLS/response-header timeouts, and the auth
	// and workspace round-trippers), but without the overall Timeout that would
	// cap the duration of a large artifact transfer.
	streamClient := *wrapped
	streamClient.Timeout = 0

	// Resolve the streaming response-header fallback: 0 means "use the generous
	// default", a negative value means "disable the fallback and rely solely on
	// the request context".
	streamHeaderTimeout := cfg.StreamHeaderTimeout
	switch {
	case streamHeaderTimeout == 0:
		streamHeaderTimeout = defaultStreamHeaderTimeout
	case streamHeaderTimeout < 0:
		streamHeaderTimeout = 0
	}

	return &Client{
		baseURL:             baseURL,
		headers:             cfg.Headers,
		httpClient:          wrapped,
		streamClient:        &streamClient,
		streamHeaderTimeout: streamHeaderTimeout,
		logger:              cfg.Logger,
	}, nil
}

// Get performs a GET request to the specified path with query parameters.
func (c *Client) Get(ctx context.Context, path string, query url.Values, result any) error {
	return c.do(ctx, http.MethodGet, c.buildURL(path, query), nil, result)
}

// GetEscaped performs a GET request where escapedPath is already
// percent-encoded (e.g. via url.PathEscape). The encoding is preserved so a
// single path segment containing reserved characters is neither re-encoded nor
// split into multiple segments.
func (c *Client) GetEscaped(ctx context.Context, escapedPath string, query url.Values, result any) error {
	reqURL, err := c.buildEscapedURL(escapedPath, query)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodGet, reqURL, nil, result)
}

// Post performs a POST request to the specified path with a JSON body.
func (c *Client) Post(ctx context.Context, path string, body, result any) error {
	return c.do(ctx, http.MethodPost, c.buildURL(path, nil), body, result)
}

// Delete performs a DELETE request to the specified path with a JSON body.
func (c *Client) Delete(ctx context.Context, path string, body, result any) error {
	return c.do(ctx, http.MethodDelete, c.buildURL(path, nil), body, result)
}

// DeleteEscaped performs a DELETE request where escapedPath is already
// percent-encoded (e.g. via url.PathEscape). See GetEscaped for the encoding
// contract.
func (c *Client) DeleteEscaped(ctx context.Context, escapedPath string, body, result any) error {
	reqURL, err := c.buildEscapedURL(escapedPath, nil)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, reqURL, body, result)
}

// Patch performs a PATCH request to the specified path with a JSON body.
func (c *Client) Patch(ctx context.Context, path string, body, result any) error {
	return c.do(ctx, http.MethodPatch, c.buildURL(path, nil), body, result)
}

// GetBytes performs a GET request and returns the raw response body.
func (c *Client) GetBytes(ctx context.Context, path string, query url.Values) ([]byte, string, error) {
	return c.doRaw(ctx, http.MethodGet, path, query, nil, "", false)
}

// GetBody performs a GET request and returns the response body for streaming.
// The returned stream is capped at maxResponseBodySize. The caller must close
// the returned ReadCloser.
func (c *Client) GetBody(ctx context.Context, path string, query url.Values) (io.ReadCloser, error) {
	return c.doRawBody(ctx, http.MethodGet, path, query, nil, "", false, true)
}

// GetBodyStream performs a GET request and returns the response body for
// streaming without capping the response size. Use this for artifact downloads,
// where the body is an arbitrarily large object stream the caller consumes
// incrementally rather than a buffered API response. The transfer is not subject
// to the overall http.Client.Timeout; bound it via ctx. The caller must close the
// returned ReadCloser.
func (c *Client) GetBodyStream(ctx context.Context, path string, query url.Values) (io.ReadCloser, error) {
	return c.doRawBody(ctx, http.MethodGet, path, query, nil, "", false, false)
}

// PutBytes performs a PUT request with a raw body and content type.
func (c *Client) PutBytes(ctx context.Context, path string, body []byte, contentType string) error {
	_, _, err := c.doRaw(ctx, http.MethodPut, path, nil, body, contentType, false)
	return err
}

// PutReader performs a PUT request that streams body as the request payload
// without buffering it in memory, so arbitrarily large artifacts can be
// uploaded. When body's length is not known ahead of time (e.g. an *os.File or a
// plain io.Reader) the request uses chunked transfer encoding. The upload is not
// subject to the overall http.Client.Timeout; bound it via ctx. The caller
// retains ownership of body and is responsible for closing it if needed.
func (c *Client) PutReader(ctx context.Context, path string, body io.Reader, contentType string) error {
	reqURL := c.buildURL(path, nil)

	// net/http's Transport closes the request body (even on error). Wrap an
	// io.Closer body so the caller keeps ownership, per this method's contract.
	// *bytes.Reader/*strings.Reader/*bytes.Buffer are not closers, so they stay
	// unwrapped and http.NewRequest can still detect them to set Content-Length.
	if _, ok := body.(io.Closer); ok {
		body = io.NopCloser(body)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL.String(), body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	if c.logger != nil {
		c.logger.Debug("request",
			"method", http.MethodPut,
			"url", reqURL.String(),
		)
	}

	// Bound the wait for response headers, mirroring the streaming download path,
	// but only when the caller supplied no deadline of its own. streamClient has no
	// overall Timeout and the shared Transport may set no ResponseHeaderTimeout
	// (e.g. http.DefaultTransport), so a server that consumes the whole body and
	// then never responds would otherwise block forever. The upload itself must
	// stay unbounded (a large artifact can take arbitrarily long to send), so the
	// timer starts only once the request body has been fully written: it caps the
	// wait for headers, not the transfer.
	var (
		headerMu    sync.Mutex
		headerTimer *time.Timer
		headerFired bool
		stopHeaders = func() {}
	)
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && c.streamHeaderTimeout > 0 && req.Body != nil {
		streamCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		req = req.WithContext(streamCtx)

		timeout := c.streamHeaderTimeout
		var stopped bool
		req.Body = &eofTriggerReader{ReadCloser: req.Body, onEOF: func() {
			headerMu.Lock()
			defer headerMu.Unlock()
			if stopped {
				return
			}
			headerTimer = time.AfterFunc(timeout, func() {
				headerMu.Lock()
				headerFired = true
				headerMu.Unlock()
				cancel()
			})
		}}
		stopHeaders = func() {
			headerMu.Lock()
			stopped = true
			if headerTimer != nil {
				headerTimer.Stop()
			}
			headerMu.Unlock()
		}
	}

	// Stream the (potentially very large) body with the timeout-free client so a
	// slow upload is not aborted by the overall http.Client.Timeout; the transfer
	// is bounded by ctx via the request instead. Redirect following is disabled:
	// Go turns 301/302/303 into a bodyless GET (and cannot replay a streamed body
	// for 307/308), so a followed redirect could return 200 while the server
	// stored nothing. Returning the 3xx verbatim lets the non-2xx check below
	// surface it as a failure.
	uploadClient := *c.streamClient
	uploadClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := uploadClient.Do(req)
	stopHeaders()
	if err != nil {
		headerMu.Lock()
		fired := headerFired
		headerMu.Unlock()
		if fired {
			return fmt.Errorf("request failed: response headers not received within %s", c.streamHeaderTimeout)
		}
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if c.logger != nil {
		c.logger.Debug("response",
			"status", resp.StatusCode,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}

	// The response to a PUT is small (status/metadata); read it fully so the
	// connection can be reused and any error body can be surfaced.
	respBody, err := readResponseBody(resp.Body)
	if err != nil {
		return err
	}
	// Treat anything outside 2xx as a failure. A streaming body has no
	// req.GetBody, so Go's client cannot replay it across a redirect and returns
	// the 3xx response verbatim; accepting it would report a stored-nothing
	// upload as success.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.parseError(resp.StatusCode, respBody)
	}
	return nil
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

// DoAbsoluteGetBody performs a GET to an absolute URL and returns the response body for streaming.
// The caller must close the returned ReadCloser.
func (c *Client) DoAbsoluteGetBody(ctx context.Context, absoluteURL string, headers map[string]string) (io.ReadCloser, error) {
	return c.doAbsoluteBody(ctx, http.MethodGet, absoluteURL, headers)
}

// buildURL constructs the full request URL by appending path to the base URL
// prefix. The path is treated as a literal (raw) value: reserved characters are
// percent-encoded exactly once during serialization. This preserves raw-path
// handling for callers such as the artifacts client that pass storage paths
// verbatim. Callers that need a single, already-escaped path segment (e.g. a
// name containing "/") must use buildEscapedURL instead.
func (c *Client) buildURL(path string, query url.Values) *url.URL {
	// Build the escaped request path from the base URL's escaped prefix plus the
	// escaped literal suffix, then keep it in RawPath. This:
	//   - preserves any encoding in the base prefix (e.g. https://host/api%2Fv1)
	//     rather than decoding it to raw separators;
	//   - encodes reserved characters in path exactly once (literal handling);
	//   - unlike ResolveReference, does not collapse "." or ".." segments, so
	//     storage object paths containing them are left intact.
	rawPath := strings.TrimRight(c.baseURL.EscapedPath(), "/") + (&url.URL{Path: path}).EscapedPath()
	decoded, err := url.PathUnescape(rawPath)
	if err != nil {
		// rawPath is assembled from validly-escaped parts, so this is
		// unreachable in practice; fall back defensively.
		decoded = rawPath
	}
	u := *c.baseURL
	u.RawPath = rawPath
	u.Path = decoded
	u.RawQuery = query.Encode()
	return &u
}

// buildEscapedURL constructs the full request URL from an already
// percent-encoded path (e.g. produced by url.PathEscape). RawPath preserves the
// caller's escaping so it is not double-encoded, while Path holds the decoded
// form required by net/url. Unlike buildURL, this does not treat reserved
// characters as literals: it trusts the caller to have escaped each path
// segment.
func (c *Client) buildEscapedURL(escapedPath string, query url.Values) (*url.URL, error) {
	// Use the base URL's escaped path so any encoding in the base prefix
	// (e.g. https://host/api%2Fv1) is preserved rather than decoded into raw
	// separators when assigned to RawPath below.
	rawPath := strings.TrimRight(c.baseURL.EscapedPath(), "/") + escapedPath
	decoded, err := url.PathUnescape(rawPath)
	if err != nil {
		return nil, fmt.Errorf("invalid escaped path %q: %w", escapedPath, err)
	}
	u := *c.baseURL
	u.RawPath = rawPath
	u.Path = decoded
	u.RawQuery = query.Encode()
	return &u, nil
}

func (c *Client) do(ctx context.Context, method string, reqURL *url.URL, body, result any) error {
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

	// Read response body (bounded by maxResponseBodySize)
	respBody, err := readResponseBody(resp.Body)
	if err != nil {
		return err
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
	reqURL := c.buildURL(path, query)

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

func (c *Client) doRawBody(ctx context.Context, method, path string, query url.Values, body []byte, contentType string, jsonAccept, capResponse bool) (io.ReadCloser, error) {
	reqURL := c.buildURL(path, query)

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
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

	if c.logger != nil {
		c.logger.Debug("request",
			"method", method,
			"url", reqURL.String(),
		)
	}

	return c.doRequestBody(req, capResponse)
}

func (c *Client) doAbsoluteBody(ctx context.Context, method, absoluteURL string, headers map[string]string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, method, absoluteURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	if c.logger != nil {
		c.logger.Debug("request",
			"method", method,
			"url", redactAbsoluteURLForLog(absoluteURL),
		)
	}

	// Absolute GETs are presigned artifact downloads: arbitrarily large object
	// streams the caller consumes incrementally, so they are not size-capped.
	return c.doRequestBody(req, false)
}

func (c *Client) doRequestBody(req *http.Request, capResponse bool) (io.ReadCloser, error) {
	start := time.Now()

	// Uncapped bodies are artifact streams read incrementally by the caller; use
	// the timeout-free stream client so a large transfer is not aborted by the
	// overall http.Client.Timeout. Bounded reads keep the capped API client.
	client := c.httpClient
	// headerCancel bounds the response-header phase of a streaming request. The
	// stream client has no overall Timeout and its shared Transport may set no
	// ResponseHeaderTimeout (e.g. http.DefaultTransport), so without this a server
	// that accepts the connection but never sends headers would block forever.
	//
	// The bound is only a fallback: when the caller's context already carries a
	// deadline, that deadline governs the whole request (headers included) and we
	// add no timer. Otherwise we fall back to streamHeaderTimeout, which is
	// independent of the API Timeout — MLflow's mlflow-artifacts proxy fetches the
	// full remote object before sending headers, so a large artifact can take far
	// longer than the 30s API timeout to reach first byte. The timer fires cancel
	// if headers do not arrive in time; once they do we stop the timer and hand
	// cancel to the body's Close so the transfer stays bound only by the context.
	var headerCancel context.CancelFunc
	var headerTimer *time.Timer
	var headerTimeout time.Duration
	if !capResponse {
		client = c.streamClient
		_, hasDeadline := req.Context().Deadline()
		if !hasDeadline && c.streamHeaderTimeout > 0 {
			headerTimeout = c.streamHeaderTimeout
			var ctx context.Context
			ctx, headerCancel = context.WithCancel(req.Context())
			req = req.WithContext(ctx)
			headerTimer = time.AfterFunc(headerTimeout, headerCancel)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		// If the header timer fired it canceled the request context, so Do failed
		// with context.Canceled. Stop reporting false then means the timer fired;
		// surface it as a header timeout so the caller can tell it apart from its
		// own cancellation, matching the success path and PutReader.
		fired := headerTimer != nil && !headerTimer.Stop()
		if headerCancel != nil {
			headerCancel()
		}
		if fired {
			return nil, fmt.Errorf("request failed: response headers not received within %s", headerTimeout)
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if headerTimer != nil && !headerTimer.Stop() {
		// The header deadline fired (Stop reports it did not stop a pending timer)
		// after Do returned success, so headerCancel has already canceled the
		// request context. The response body is tied to that context and its first
		// read would fail, so treat this as a header timeout rather than handing
		// back a stream that is already broken.
		resp.Body.Close()
		headerCancel()
		return nil, fmt.Errorf("request failed: response headers not received within %s", headerTimeout)
	}

	if c.logger != nil {
		c.logger.Debug("response",
			"status", resp.StatusCode,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}

	if resp.StatusCode >= 400 {
		respBody, readErr := readResponseBody(resp.Body)
		resp.Body.Close()
		if headerCancel != nil {
			headerCancel()
		}
		if readErr != nil {
			return nil, readErr
		}
		return nil, c.parseError(resp.StatusCode, respBody)
	}

	if !capResponse {
		if headerCancel != nil {
			// Keep the header-phase context alive for the duration of the stream;
			// release it when the caller closes the body.
			return &cancelReadCloser{ReadCloser: resp.Body, cancel: headerCancel}, nil
		}
		return resp.Body, nil
	}
	return newLimitedReadCloser(resp.Body, maxResponseBodySize), nil
}

// eofTriggerReader wraps a request body and runs onEOF once, the first time the
// underlying reader reports io.EOF (i.e. the body has been fully sent). PutReader
// uses it to start the response-header timeout only after the upload completes, so
// a slow or large transfer is not cut off mid-flight.
type eofTriggerReader struct {
	io.ReadCloser
	onEOF func()
	once  sync.Once
}

func (e *eofTriggerReader) Read(p []byte) (int, error) {
	n, err := e.ReadCloser.Read(p)
	if err == io.EOF {
		e.once.Do(e.onEOF)
	}
	return n, err
}

// cancelReadCloser wraps a response body and runs cancel when the body is closed,
// releasing a context created to bound the request's response-header phase.
type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
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

type limitedReadCloser struct {
	r        io.Reader
	closer   io.Closer
	limit    int64
	read     int64
	exceeded bool
}

func newLimitedReadCloser(body io.ReadCloser, limit int64) io.ReadCloser {
	return &limitedReadCloser{
		r:      io.LimitReader(body, limit+1),
		closer: body,
		limit:  limit,
	}
}

func (l *limitedReadCloser) Read(p []byte) (int, error) {
	if l.exceeded {
		return 0, fmt.Errorf("response body exceeds maximum size of %d bytes", l.limit)
	}

	n, err := l.r.Read(p)
	l.read += int64(n)
	if l.read > l.limit {
		// Exclude the overflow sentinel byte(s) past the configured limit.
		over := l.read - l.limit
		n -= int(over)
		if n < 0 {
			n = 0
		}
		l.read = l.limit
		l.exceeded = true
		return n, fmt.Errorf("response body exceeds maximum size of %d bytes", l.limit)
	}

	return n, err
}

func (l *limitedReadCloser) Close() error {
	return l.closer.Close()
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

func wrapClientWithAuth(c *http.Client, token, tokenPath string, trackingURL *url.URL) *http.Client {
	if token == "" && tokenPath == "" {
		return c
	}
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	var rt http.RoundTripper
	if tokenPath != "" {
		rt = NewTokenFileRoundTripper(base, tokenPath, trackingURL)
	} else {
		rt = NewTokenRoundTripper(base, token, trackingURL)
	}
	clone := *c
	clone.Transport = rt
	return &clone
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
