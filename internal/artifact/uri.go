package artifact

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

const (
	mlflowArtifactsScheme      = "mlflow-artifacts"
	artifactsAPIPrefix         = "/api/2.0/mlflow-artifacts/artifacts"
	trackingServerUploadPath   = "/ajax-api/2.0/mlflow/upload-artifact"
	trackingServerDownloadPath = "/get-artifact"

	// MaxTrackingServerUploadSize is the hard limit enforced by MLflow's legacy
	// upload_artifact_handler (/ajax-api/2.0/mlflow/upload-artifact). Verified
	// against the pinned PROTO_VERSION (MLflow v3.12.0): 10 * 1024 * 1024.
	MaxTrackingServerUploadSize = 10 << 20 // 10 MiB
)

// ResolveStoragePath maps a run artifact URI and relative artifact path to the
// storage path used by the mlflow-artifacts proxy API.
func ResolveStoragePath(artifactURI, artifactPath string) (string, error) {
	if artifactURI == "" {
		return "", fmt.Errorf("mlflow: artifact URI is required")
	}

	parsed, err := url.Parse(artifactURI)
	if err != nil {
		return "", fmt.Errorf("invalid artifact URI: %w", err)
	}

	var basePath string
	switch parsed.Scheme {
	case mlflowArtifactsScheme:
		basePath = strings.TrimPrefix(parsed.Path, "/")
	case "http", "https":
		if !strings.Contains(parsed.Path, artifactsAPIPrefix) {
			return "", fmt.Errorf("mlflow: unsupported HTTP artifact URI %q for proxy access", artifactURI)
		}
		idx := strings.Index(parsed.Path, artifactsAPIPrefix)
		basePath = strings.TrimPrefix(parsed.Path[idx+len(artifactsAPIPrefix):], "/")
	default:
		return "", fmt.Errorf("mlflow: unsupported artifact URI scheme %q for proxy access", parsed.Scheme)
	}

	if basePath == "" {
		return "", fmt.Errorf("mlflow: artifact URI path is required")
	}

	basePath = path.Clean(basePath)
	if basePath == "." || basePath == ".." ||
		path.IsAbs(basePath) || strings.HasPrefix(basePath, "../") {
		return "", fmt.Errorf("mlflow: artifact URI path escapes the artifact root")
	}

	if artifactPath == "" {
		return basePath, nil
	}

	cleaned := path.Join(basePath, artifactPath)
	if !strings.HasPrefix(cleaned, basePath+"/") && cleaned != basePath {
		return "", fmt.Errorf("mlflow: artifact path %q escapes the run artifact directory", artifactPath)
	}
	return cleaned, nil
}

// PresignedStoragePath maps a run artifact URI and relative artifact path to the
// storage path used by the presigned download API. Proxied URIs use proxy path
// resolution; cloud URIs derive the object key prefix from the URI path.
func PresignedStoragePath(artifactURI, artifactPath string) (string, error) {
	if IsProxied(artifactURI) {
		return ResolveStoragePath(artifactURI, artifactPath)
	}

	parsed, err := url.Parse(artifactURI)
	if err != nil {
		return "", fmt.Errorf("invalid artifact URI: %w", err)
	}

	switch parsed.Scheme {
	case "s3", "gs", "gcs":
		basePath := strings.TrimPrefix(parsed.Path, "/")
		if basePath == "" {
			return "", fmt.Errorf("mlflow: artifact URI path is required")
		}

		basePath = path.Clean(basePath)
		if basePath == "." || basePath == ".." ||
			path.IsAbs(basePath) || strings.HasPrefix(basePath, "../") {
			return "", fmt.Errorf("mlflow: artifact URI path escapes the artifact root")
		}

		if artifactPath == "" {
			return basePath, nil
		}

		cleaned := path.Join(basePath, artifactPath)
		if !strings.HasPrefix(cleaned, basePath+"/") && cleaned != basePath {
			return "", fmt.Errorf("mlflow: artifact path %q escapes the run artifact directory", artifactPath)
		}
		return cleaned, nil
	default:
		return "", fmt.Errorf("mlflow: unsupported artifact URI scheme %q for presigned download", parsed.Scheme)
	}
}

// SupportsTrackingServerArtifacts reports whether artifacts are stored on the
// tracking server filesystem and can be uploaded/downloaded via legacy server routes.
func SupportsTrackingServerArtifacts(artifactURI string) bool {
	if artifactURI == "" || IsProxied(artifactURI) {
		return false
	}

	parsed, err := url.Parse(artifactURI)
	if err != nil {
		return false
	}

	switch parsed.Scheme {
	case "", "file":
		return true
	default:
		return false
	}
}

// IsProxied reports whether artifact upload/download can use the mlflow-artifacts proxy.
func IsProxied(artifactURI string) bool {
	if artifactURI == "" {
		return false
	}

	parsed, err := url.Parse(artifactURI)
	if err != nil {
		return false
	}

	switch parsed.Scheme {
	case mlflowArtifactsScheme:
		return true
	case "http", "https":
		return strings.Contains(parsed.Path, artifactsAPIPrefix)
	default:
		return false
	}
}

// ProxyUploadPath returns the PUT path for a proxied artifact upload.
func ProxyUploadPath(storagePath string) string {
	return path.Join(artifactsAPIPrefix, storagePath)
}

// ProxyDownloadPath returns the GET path for a proxied artifact download.
func ProxyDownloadPath(storagePath string) string {
	return path.Join(artifactsAPIPrefix, storagePath)
}

// PresignedDownloadPath returns the GET path for a presigned download URL request.
func PresignedDownloadPath(storagePath string) string {
	return path.Join("/api/2.0/mlflow-artifacts/presigned", storagePath)
}
