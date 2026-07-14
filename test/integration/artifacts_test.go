//go:build integration

package integration

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/opendatahub-io/mlflow-go/mlflow"
	"github.com/opendatahub-io/mlflow-go/mlflow/artifacts"
	"github.com/opendatahub-io/mlflow-go/mlflow/tracking"
)

// TestArtifactRoundTrip tests upload, list, and download of run artifacts.
func TestArtifactRoundTrip(t *testing.T) {
	client, err := mlflow.NewClient(mlflow.WithInsecure())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	expName := fmt.Sprintf("e2e-artifacts-exp-%d", time.Now().UnixNano())
	expID, err := client.Tracking().CreateExperiment(ctx, expName)
	if err != nil {
		t.Fatalf("CreateExperiment() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Tracking().DeleteExperiment(ctx, expID) })

	run, err := client.Tracking().CreateRun(ctx, expID, tracking.WithRunName("artifact-run"))
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Tracking().DeleteRun(ctx, run.Info.RunID) })

	content := []byte(fmt.Sprintf("artifact-content-%d", time.Now().UnixNano()))
	artifactPath := "metrics/output.txt"

	t.Log("Step 1: Upload artifact")
	err = client.Artifacts().LogArtifact(ctx, run.Info.RunID, artifactPath, bytes.NewReader(content), artifacts.WithContentType("text/plain"))
	if err != nil {
		t.Fatalf("LogArtifact() error = %v", err)
	}

	t.Log("Step 2: List artifacts")
	listResult, err := client.Artifacts().ListArtifacts(ctx, run.Info.RunID, artifacts.WithArtifactPath("metrics"))
	if err != nil {
		t.Fatalf("ListArtifacts() error = %v", err)
	}

	found := false
	for _, file := range listResult.Files {
		if file.Path == artifactPath {
			found = true
			if file.IsDir {
				t.Errorf("artifact %q should not be a directory", artifactPath)
			}
			if file.FileSize != int64(len(content)) {
				t.Errorf("FileSize = %d, want %d", file.FileSize, len(content))
			}
		}
	}
	if !found {
		t.Fatalf("artifact %q not found in list: %+v", artifactPath, listResult.Files)
	}

	t.Log("Step 3: Download artifact")
	downloaded, err := client.Artifacts().DownloadArtifact(ctx, run.Info.RunID, artifactPath)
	if err != nil {
		t.Fatalf("DownloadArtifact() error = %v", err)
	}
	if !bytes.Equal(downloaded, content) {
		t.Errorf("downloaded content = %q, want %q", string(downloaded), string(content))
	}
}
