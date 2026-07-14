package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/opendatahub-io/mlflow-go/internal/conv"
	internalerrors "github.com/opendatahub-io/mlflow-go/internal/errors"
	"github.com/opendatahub-io/mlflow-go/internal/gen/artifactspb"
	"github.com/opendatahub-io/mlflow-go/internal/gen/mlflowpb"
	"github.com/opendatahub-io/mlflow-go/internal/transport"
)

const (
	presignedUploadPath = "/api/2.0/mlflow/artifacts/presigned-upload-url"
)

// Store uploads and downloads run artifacts using presigned URLs with proxy fallback.
type Store struct {
	transport *transport.Client
}

// NewStore creates a new artifact store.
func NewStore(t *transport.Client) *Store {
	return &Store{transport: t}
}

// UploadOptions configures artifact upload behavior.
type UploadOptions struct {
	ContentType string
	Expiration  int64
}

// Upload uploads artifact bytes for a run, preferring presigned URLs when available.
func (s *Store) Upload(ctx context.Context, runID, artifactURI, artifactPath string, content []byte, opts UploadOptions) error {
	if runID == "" {
		return fmt.Errorf("mlflow: run ID is required")
	}
	if artifactPath == "" {
		return fmt.Errorf("mlflow: artifact path is required")
	}

	contentType := opts.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	err := s.uploadPresigned(ctx, runID, artifactPath, content, contentType, opts.Expiration)
	if err == nil {
		return nil
	}
	if !shouldFallbackFromPresigned(err) {
		return err
	}

	if IsProxied(artifactURI) {
		storagePath, resolveErr := ResolveStoragePath(artifactURI, artifactPath)
		if resolveErr != nil {
			return resolveErr
		}

		if err := s.transport.PutBytes(ctx, ProxyUploadPath(storagePath), content, contentType); err != nil {
			return fmt.Errorf("failed to upload artifact via proxy: %w", err)
		}

		return nil
	}

	if SupportsTrackingServerArtifacts(artifactURI) {
		if err := s.uploadViaTrackingServer(ctx, runID, artifactPath, content, contentType); err != nil {
			return fmt.Errorf("failed to upload artifact via tracking server: %w", err)
		}

		return nil
	}

	return fmt.Errorf("failed to upload artifact: presigned upload unavailable and artifact URI %q does not support proxy or tracking-server upload", artifactURI)
}

// DownloadOptions configures artifact download behavior.
type DownloadOptions struct {
	Expiration int64
}

// Download downloads artifact bytes for a run, preferring presigned URLs when available.
func (s *Store) Download(ctx context.Context, runID, artifactURI, artifactPath string, opts DownloadOptions) ([]byte, error) {
	if runID == "" {
		return nil, fmt.Errorf("mlflow: run ID is required")
	}
	if artifactURI == "" {
		return nil, fmt.Errorf("mlflow: artifact URI is required")
	}
	if artifactPath == "" {
		return nil, fmt.Errorf("mlflow: artifact path is required")
	}

	if SupportsTrackingServerArtifacts(artifactURI) {
		data, err := s.downloadViaTrackingServer(ctx, runID, artifactPath)
		if err != nil {
			return nil, fmt.Errorf("failed to download artifact via tracking server: %w", err)
		}

		return data, nil
	}

	if !IsProxied(artifactURI) {
		return nil, fmt.Errorf("failed to download artifact: artifact URI %q does not support proxy or tracking-server download", artifactURI)
	}

	storagePath, err := ResolveStoragePath(artifactURI, artifactPath)
	if err != nil {
		return nil, err
	}

	data, err := s.downloadPresigned(ctx, storagePath, opts.Expiration)
	if err == nil {
		return data, nil
	}
	if !shouldFallbackFromPresigned(err) {
		return nil, err
	}

	data, _, err = s.transport.GetBytes(ctx, ProxyDownloadPath(storagePath), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to download artifact via proxy: %w", err)
	}

	return data, nil
}

// ArtifactURI loads the artifact URI for a run.
func (s *Store) ArtifactURI(ctx context.Context, runID string) (string, error) {
	if runID == "" {
		return "", fmt.Errorf("mlflow: run ID is required")
	}

	query := url.Values{"run_id": []string{runID}}

	var resp mlflowpb.GetRun_Response
	if err := s.transport.Get(ctx, "/api/2.0/mlflow/runs/get", query, &resp); err != nil {
		return "", fmt.Errorf("failed to get run: %w", err)
	}

	if resp.Run == nil || resp.Run.Info == nil {
		return "", fmt.Errorf("failed to get run: empty response")
	}

	artifactURI := resp.Run.Info.GetArtifactUri()
	if artifactURI == "" {
		return "", fmt.Errorf("mlflow: run has no artifact URI")
	}

	return artifactURI, nil
}

func (s *Store) uploadPresigned(ctx context.Context, runID, artifactPath string, content []byte, contentType string, expiration int64) error {
	req := &mlflowpb.CreatePresignedUploadUrl{
		RunId: conv.Ptr(runID),
		Path:  conv.Ptr(artifactPath),
	}
	if expiration > 0 {
		req.Expiration = conv.Ptr(expiration)
	}

	var resp mlflowpb.CreatePresignedUploadUrl_Response
	if err := s.transport.Post(ctx, presignedUploadPath, req, &resp); err != nil {
		return err
	}

	presignedURL := resp.GetPresignedUrl()
	if presignedURL == "" {
		return fmt.Errorf("mlflow: presigned upload URL is empty")
	}

	headers := map[string]string{"Content-Type": contentType}
	for k, v := range resp.GetHeaders() {
		headers[k] = v
	}

	if _, _, err := s.transport.DoAbsolute(ctx, http.MethodPut, presignedURL, headers, content); err != nil {
		return fmt.Errorf("failed to upload artifact to presigned URL: %w", err)
	}

	return nil
}

func (s *Store) downloadPresigned(ctx context.Context, storagePath string, expiration int64) ([]byte, error) {
	query := url.Values{}
	if expiration > 0 {
		query.Set("expiration", fmt.Sprintf("%d", expiration))
	}

	respBody, _, err := s.transport.GetBytes(ctx, PresignedDownloadPath(storagePath), query)
	if err != nil {
		return nil, err
	}

	var resp artifactspb.GetPresignedDownloadUrl_Response
	if unmarshalErr := unmarshalJSON(respBody, &resp); unmarshalErr != nil {
		return nil, fmt.Errorf("failed to decode presigned download response: %w", unmarshalErr)
	}

	downloadURL := resp.GetUrl()
	if downloadURL == "" {
		return nil, fmt.Errorf("mlflow: presigned download URL is empty")
	}

	headers := map[string]string{}
	for k, v := range resp.GetHeaders() {
		headers[k] = v
	}

	data, _, err := s.transport.DoAbsolute(ctx, http.MethodGet, downloadURL, headers, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to download artifact from presigned URL: %w", err)
	}

	return data, nil
}

func (s *Store) uploadViaTrackingServer(ctx context.Context, runID, artifactPath string, content []byte, contentType string) error {
	query := url.Values{
		"run_uuid": []string{runID},
		"path":     []string{artifactPath},
	}
	return s.transport.PostBytes(ctx, trackingServerUploadPath, query, content, contentType)
}

func (s *Store) downloadViaTrackingServer(ctx context.Context, runID, artifactPath string) ([]byte, error) {
	query := url.Values{
		"run_id": []string{runID},
		"path":   []string{artifactPath},
	}
	data, _, err := s.transport.GetBytes(ctx, trackingServerDownloadPath, query)
	return data, err
}

func shouldFallbackFromPresigned(err error) bool {
	if err == nil {
		return false
	}
	if internalerrors.IsNotFound(err) || internalerrors.IsInvalidArgument(err) {
		return true
	}

	var apiErr *internalerrors.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusNotFound, http.StatusBadRequest, http.StatusNotImplemented, http.StatusMethodNotAllowed:
			return true
		}
	}

	return false
}

func unmarshalJSON(data []byte, result any) error {
	if len(data) == 0 {
		return io.EOF
	}
	return json.Unmarshal(data, result)
}
