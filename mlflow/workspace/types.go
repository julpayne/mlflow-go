// Package workspace provides types for MLflow workspace management.
package workspace

// ServerInfo represents the response from the MLflow server-info endpoint.
type ServerInfo struct {
	WorkspacesEnabled bool
}

// Workspace represents an MLflow workspace.
type Workspace struct {
	Name string
}
