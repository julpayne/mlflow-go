package artifacts

import (
	"github.com/opendatahub-io/mlflow-go/internal/gen/mlflowpb"
)

// FileInfo describes a single artifact file or directory.
type FileInfo struct {
	Path     string
	IsDir    bool
	FileSize int64
}

// ListArtifactsResult is the response from ListArtifacts.
type ListArtifactsResult struct {
	RootURI       string
	Files         []FileInfo
	NextPageToken string
}

func fileInfoFromProto(f *mlflowpb.FileInfo) FileInfo {
	if f == nil {
		return FileInfo{}
	}

	return FileInfo{
		Path:     f.GetPath(),
		IsDir:    f.GetIsDir(),
		FileSize: f.GetFileSize(),
	}
}

func listArtifactsResultFromProto(resp *mlflowpb.ListArtifacts_Response) *ListArtifactsResult {
	if resp == nil {
		return &ListArtifactsResult{}
	}

	result := &ListArtifactsResult{
		RootURI:       resp.GetRootUri(),
		NextPageToken: resp.GetNextPageToken(),
	}

	for _, f := range resp.GetFiles() {
		result.Files = append(result.Files, fileInfoFromProto(f))
	}

	return result
}
