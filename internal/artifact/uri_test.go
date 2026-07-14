package artifact

import (
	"testing"
)

func TestResolveStoragePath(t *testing.T) {
	tests := []struct {
		name         string
		artifactURI  string
		artifactPath string
		want         string
		wantErr      bool
	}{
		{
			name:         "mlflow-artifacts scheme",
			artifactURI:  "mlflow-artifacts:/experiments/1/runs/abc/artifacts",
			artifactPath: "model.pkl",
			want:         "experiments/1/runs/abc/artifacts/model.pkl",
		},
		{
			name:         "http proxy uri",
			artifactURI:  "http://localhost:5000/api/2.0/mlflow-artifacts/artifacts/experiments/1/runs/abc/artifacts",
			artifactPath: "dir/file.txt",
			want:         "experiments/1/runs/abc/artifacts/dir/file.txt",
		},
		{
			name:         "empty artifact path",
			artifactURI:  "mlflow-artifacts:/experiments/1/runs/abc/artifacts",
			artifactPath: "",
			want:         "experiments/1/runs/abc/artifacts",
		},
		{
			name:         "unsupported file scheme",
			artifactURI:  "file:///tmp/artifacts",
			artifactPath: "model.pkl",
			wantErr:      true,
		},
		{
			name:         "empty artifact uri",
			artifactURI:  "",
			artifactPath: "model.pkl",
			wantErr:      true,
		},
		{
			name:         "artifact path escapes run directory",
			artifactURI:  "mlflow-artifacts:/experiments/1/runs/abc/artifacts",
			artifactPath: "../outside.txt",
			wantErr:      true,
		},
		{
			name:         "artifact path traversal via join",
			artifactURI:  "mlflow-artifacts:/experiments/1/runs/abc/artifacts",
			artifactPath: "metrics/../../outside.txt",
			wantErr:      true,
		},
		{
			name:         "malformed base path escapes artifact root",
			artifactURI:  "mlflow-artifacts:/..",
			artifactPath: "",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveStoragePath(tt.artifactURI, tt.artifactPath)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveStoragePath() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("ResolveStoragePath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPresignedStoragePath(t *testing.T) {
	tests := []struct {
		name         string
		artifactURI  string
		artifactPath string
		want         string
		wantErr      bool
	}{
		{
			name:         "s3 uri",
			artifactURI:  "s3://bucket/experiments/1/runs/abc/artifacts",
			artifactPath: "metrics.txt",
			want:         "experiments/1/runs/abc/artifacts/metrics.txt",
		},
		{
			name:         "unsupported scheme",
			artifactURI:  "ftp://host/path",
			artifactPath: "file.txt",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PresignedStoragePath(tt.artifactURI, tt.artifactPath)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("PresignedStoragePath() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("PresignedStoragePath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSupportsTrackingServerArtifacts(t *testing.T) {
	tests := []struct {
		artifactURI string
		want        bool
	}{
		{"/tmp/mlruns/1/run-1/artifacts", true},
		{"file:///tmp/mlruns/1/run-1/artifacts", true},
		{"mlflow-artifacts:/experiments/1/runs/abc/artifacts", false},
		{"http://localhost:5000/api/2.0/mlflow-artifacts/artifacts/experiments/1/runs/abc/artifacts", false},
		{"s3://bucket/path", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := SupportsTrackingServerArtifacts(tt.artifactURI); got != tt.want {
			t.Errorf("SupportsTrackingServerArtifacts(%q) = %v, want %v", tt.artifactURI, got, tt.want)
		}
	}
}

func TestIsProxied(t *testing.T) {
	tests := []struct {
		artifactURI string
		want        bool
	}{
		{"mlflow-artifacts:/experiments/1/runs/abc/artifacts", true},
		{"http://localhost:5000/api/2.0/mlflow-artifacts/artifacts/experiments/1/runs/abc/artifacts", true},
		{"file:///tmp/artifacts", false},
		{"s3://bucket/path", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := IsProxied(tt.artifactURI); got != tt.want {
			t.Errorf("IsProxied(%q) = %v, want %v", tt.artifactURI, got, tt.want)
		}
	}
}
