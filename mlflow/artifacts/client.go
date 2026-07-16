package artifacts

import (
	"context"
	"fmt"
	"io"
	"net/url"

	"github.com/opendatahub-io/mlflow-go/internal/artifact"
	"github.com/opendatahub-io/mlflow-go/internal/gen/mlflowpb"
	"github.com/opendatahub-io/mlflow-go/internal/transport"
)

const maxArtifactUploadSize = 100 << 20 // 100 MiB

// Client provides access to MLflow run artifacts.
// It is safe for concurrent use.
type Client struct {
	transport *transport.Client
	store     *artifact.Store
}

// NewClient creates a new Artifacts client.
// This is typically called internally by the root mlflow.Client.
func NewClient(t *transport.Client) *Client {
	return &Client{
		transport: t,
		store:     artifact.NewStore(t),
	}
}

// ListArtifacts lists artifacts logged for a run.
func (c *Client) ListArtifacts(ctx context.Context, runID string, opts ...ListArtifactsOption) (*ListArtifactsResult, error) {
	if runID == "" {
		return nil, fmt.Errorf("mlflow: run ID is required")
	}

	o := &listArtifactsOptions{}
	for _, opt := range opts {
		opt(o)
	}

	query := url.Values{"run_id": []string{runID}}
	if o.path != "" {
		query.Set("path", o.path)
	}
	if o.pageToken != "" {
		query.Set("page_token", o.pageToken)
	}

	var resp mlflowpb.ListArtifacts_Response
	if err := c.transport.Get(ctx, "/api/2.0/mlflow/artifacts/list", query, &resp); err != nil {
		return nil, fmt.Errorf("failed to list artifacts: %w", err)
	}

	return listArtifactsResultFromProto(&resp), nil
}

// LogArtifact uploads a single artifact file to a run.
func (c *Client) LogArtifact(ctx context.Context, runID, artifactPath string, r io.Reader, opts ...LogArtifactOption) error {
	if runID == "" {
		return fmt.Errorf("mlflow: run ID is required")
	}
	if artifactPath == "" {
		return fmt.Errorf("mlflow: artifact path is required")
	}
	if r == nil {
		return fmt.Errorf("mlflow: artifact reader is required")
	}

	o := &logArtifactOptions{}
	for _, opt := range opts {
		opt(o)
	}

	artifactURI, err := c.store.ArtifactURI(ctx, runID)
	if err != nil {
		return err
	}

	maxSize := int64(maxArtifactUploadSize)
	if artifact.SupportsTrackingServerArtifacts(artifactURI) {
		// Legacy /ajax-api/2.0/mlflow/upload-artifact rejects bodies over 10 MiB
		// (MLflow v3.12.0 upload_artifact_handler). Cap before buffering.
		maxSize = artifact.MaxTrackingServerUploadSize
	}

	content, err := readArtifactContent(r, maxSize)
	if err != nil {
		return err
	}

	uploadOpts := artifact.UploadOptions{
		ContentType: o.contentType,
		Expiration:  o.expiration,
	}
	if err := c.store.Upload(ctx, runID, artifactURI, artifactPath, content, uploadOpts); err != nil {
		return fmt.Errorf("failed to log artifact: %w", err)
	}

	return nil
}

// DownloadArtifact opens a single artifact file from a run for streaming download.
// The caller must close the returned ReadCloser.
func (c *Client) DownloadArtifact(ctx context.Context, runID, artifactPath string, opts ...DownloadArtifactOption) (io.ReadCloser, error) {
	if runID == "" {
		return nil, fmt.Errorf("mlflow: run ID is required")
	}
	if artifactPath == "" {
		return nil, fmt.Errorf("mlflow: artifact path is required")
	}

	o := &downloadArtifactOptions{}
	for _, opt := range opts {
		opt(o)
	}

	artifactURI, err := c.store.ArtifactURI(ctx, runID)
	if err != nil {
		return nil, err
	}

	downloadOpts := artifact.DownloadOptions{Expiration: o.expiration}
	rc, err := c.store.Download(ctx, runID, artifactURI, artifactPath, downloadOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to download artifact: %w", err)
	}

	return rc, nil
}

func readArtifactContent(r io.Reader, maxSize int64) ([]byte, error) {
	limited := io.LimitReader(r, maxSize+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("failed to read artifact content: %w", err)
	}
	if int64(len(content)) > maxSize {
		return nil, fmt.Errorf("artifact content exceeds maximum upload size of %d bytes", maxSize)
	}
	return content, nil
}
