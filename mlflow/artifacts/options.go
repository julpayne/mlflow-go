package artifacts

type listArtifactsOptions struct {
	path      string
	pageToken string
}

// ListArtifactsOption configures ListArtifacts.
type ListArtifactsOption func(*listArtifactsOptions)

// WithArtifactPath filters artifacts matching a relative path prefix.
func WithArtifactPath(path string) ListArtifactsOption {
	return func(o *listArtifactsOptions) {
		o.path = path
	}
}

// WithPageToken requests the next page of results.
func WithPageToken(token string) ListArtifactsOption {
	return func(o *listArtifactsOptions) {
		o.pageToken = token
	}
}

type logArtifactOptions struct {
	contentType string
	expiration  int64
}

// LogArtifactOption configures LogArtifact.
type LogArtifactOption func(*logArtifactOptions)

// WithContentType sets the Content-Type header for artifact upload.
func WithContentType(contentType string) LogArtifactOption {
	return func(o *logArtifactOptions) {
		o.contentType = contentType
	}
}

// WithExpiration sets the presigned URL expiration in seconds.
func WithExpiration(seconds int64) LogArtifactOption {
	return func(o *logArtifactOptions) {
		o.expiration = seconds
	}
}

type downloadArtifactOptions struct {
	expiration int64
}

// DownloadArtifactOption configures DownloadArtifact.
type DownloadArtifactOption func(*downloadArtifactOptions)

// WithDownloadExpiration sets the presigned download URL expiration in seconds.
func WithDownloadExpiration(seconds int64) DownloadArtifactOption {
	return func(o *downloadArtifactOptions) {
		o.expiration = seconds
	}
}
