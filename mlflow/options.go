package mlflow

import (
	"log/slog"
	"maps"
	"net/http"
	"time"
)

// options holds the configuration for a Client.
type options struct {
	trackingURI        string
	headers            map[string]string
	httpClient         *http.Client
	logger             *slog.Logger
	insecure           bool
	timeout            time.Duration
	token              string
	tokenPath          string
	workspace          string
	workspacesSupport  bool
}

// Option configures a Client.
type Option func(*options)

// WithTrackingURI sets the MLflow server URL.
// Overrides MLFLOW_TRACKING_URI environment variable.
func WithTrackingURI(uri string) Option {
	return func(o *options) {
		o.trackingURI = uri
	}
}

// WithHeaders sets custom HTTP headers sent on every API request.
// Use this to pass workspace headers, additional auth, or other metadata.
func WithHeaders(headers map[string]string) Option {
	return func(o *options) {
		if headers != nil {
			o.headers = make(map[string]string, len(headers))
			maps.Copy(o.headers, headers)
		}
	}
}

// WithHTTPClient sets a custom HTTP client.
// Use this to configure timeouts, TLS, or proxies.
// When a custom client is provided, WithTimeout is ignored;
// configure the timeout directly on the provided client.
func WithHTTPClient(client *http.Client) Option {
	return func(o *options) {
		o.httpClient = client
	}
}

// WithLogger sets a structured logger for debug output.
// If not set, the SDK is silent.
func WithLogger(handler slog.Handler) Option {
	return func(o *options) {
		if handler != nil {
			o.logger = slog.New(handler)
		}
	}
}

// WithInsecure allows HTTP connections (not recommended for production).
// Overrides MLFLOW_INSECURE_SKIP_TLS_VERIFY environment variable.
func WithInsecure() Option {
	return func(o *options) {
		o.insecure = true
	}
}

// WithTimeout sets the default timeout for API operations.
// Default: 30 seconds.
func WithTimeout(d time.Duration) Option {
	return func(o *options) {
		o.timeout = d
	}
}

// WithToken sets a static bearer token for authentication.
// Overrides MLFLOW_TRACKING_TOKEN environment variable.
// If the token contains a colon (user:pass), Basic auth is used;
// otherwise the token is sent as a Bearer token.
func WithToken(token string) Option {
	return func(o *options) {
		o.token = token
	}
}

// WithTokenPath sets a path to a file containing the auth token.
// The file is re-read on every request to support Kubernetes projected
// service-account tokens that rotate without process restart.
func WithTokenPath(path string) Option {
	return func(o *options) {
		o.tokenPath = path
	}
}

// WithWorkspace sets the workspace name for multi-tenant isolation.
// When combined with WithWorkspacesSupport, the X-MLFLOW-WORKSPACE header
// is only sent if the server reports that workspaces are enabled.
// Without WithWorkspacesSupport, the header is always sent.
func WithWorkspace(name string) Option {
	return func(o *options) {
		o.workspace = name
	}
}

// WithWorkspacesSupport enables server probing to conditionally attach the
// X-MLFLOW-WORKSPACE header. On the first API call, the client calls
// GET /api/3.0/mlflow/server-info to check if workspaces are enabled.
// If disabled, the workspace header is silently omitted to avoid
// FEATURE_DISABLED errors from MLflow 3.13+.
func WithWorkspacesSupport() Option {
	return func(o *options) {
		o.workspacesSupport = true
	}
}
