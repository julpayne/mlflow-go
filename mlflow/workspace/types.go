// Package workspace provides types for MLflow workspace management.
package workspace

import "time"

// ServerInfo represents the response from the MLflow server-info endpoint.
type ServerInfo struct {
	WorkspacesEnabled bool
}

// Workspace represents an MLflow workspace.
type Workspace struct {
	Name         string
	CreationTime time.Time
}

func timeFromMillis(ms int64) time.Time {
	return time.UnixMilli(ms)
}
