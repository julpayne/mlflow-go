# ADR-0010: Artifact Management Client

**Status**: Accepted

**Date**: 2026-07-14

**Authors**: @julpayne

## Context

ADR-0009 excluded artifact management from the tracking MVP. Users need to upload, list, and download run artifacts from Go applications without calling the REST API directly or using the Python SDK.

MLflow supports two artifact access patterns:

1. **Presigned URLs** — the tracking server generates signed URLs so clients upload/download directly from cloud storage (S3, GCS) without client-side credentials.
2. **mlflow-artifacts proxy** — the tracking server streams artifact bytes via `/api/2.0/mlflow-artifacts/artifacts/*` when started with `--serve-artifacts`.

## Decision

### 1. Public `mlflow/artifacts` sub-client

Add `client.Artifacts()` following ADR-0005 with three run-scoped methods aligned to the Python SDK (ADR-0007):

- `ListArtifacts(ctx, runID, opts...)`
- `LogArtifact(ctx, runID, artifactPath, r, opts...)`
- `DownloadArtifact(ctx, runID, artifactPath, opts...)`

All methods take explicit `runID` — no global active-run state (ADR-0009).

### 2. Dual transport strategy

Use tracking-server routes directly for local filesystem artifact roots (`file://` / path-only). Otherwise prefer presigned URLs with proxy fallback in `internal/artifact/store.go`:

| Operation | Primary | Fallback |
|-----------|---------|----------|
| Upload | `POST /api/2.0/mlflow/artifacts/presigned-upload-url` → external PUT | `PUT /api/2.0/mlflow-artifacts/artifacts/{path}` |
| Download | `GET /api/2.0/mlflow-artifacts/presigned/{path}` → external GET | `GET /api/2.0/mlflow-artifacts/artifacts/{path}` |
| List | `GET /api/2.0/mlflow/artifacts/list` | — |

### 3. Transport extensions

Extend `internal/transport` with `GetBytes`, `PutBytes`, and `DoAbsolute` for binary artifact I/O and presigned URL requests outside the tracking base URL.

### 4. MVP scope

Included: single-file upload/download, list with pagination, presigned URLs, proxy fallback.

Excluded (defer): multipart upload, delete, directory upload/download, logged-model artifacts, direct `s3://`/`file://` client access.

### 5. Server requirements

- **Proxy mode (local dev):** `--serve-artifacts` and `--artifacts-destination`. MLflow assigns `mlflow-artifacts:/` URIs automatically when artifact serving is enabled.
- **Direct filesystem mode:** `--no-serve-artifacts` with a filesystem `--default-artifact-root` (e.g. `./mlartifacts`); the client uses tracking-server upload/download routes directly (skips presigned).
- **Presigned mode (cloud):** MLflow 3.12+ with cloud-backed artifact store and server-side credentials. Requires no client cloud credentials.

## Alternatives Considered

### Alternative 1: Proxy-only

Simpler implementation but excludes cloud deployments where presigned URLs avoid streaming data through the tracking server.

### Alternative 2: Merge with trace-attachments branch

Rejected — trace attachments are a separate domain; run artifact management is implemented fresh on main.

### Alternative 3: Put artifact methods on Tracking client

Rejected — artifacts are a distinct MLflow domain; a separate sub-client keeps the root client interface small (ADR-0005).

## Consequences

### Positive

- Go users can manage run artifacts without Python SDK or raw REST calls
- Presigned URLs enable efficient cloud storage access without client credentials
- Proxy fallback supports local development with standard OSS MLflow server

### Negative

- `LogArtifact` reads the entire upload into memory via `io.ReadAll` — large files risk uncontrolled memory allocation (streaming deferred)
- `DownloadArtifact` returns `io.ReadCloser` for streaming download; uploads still buffer via `io.ReadAll` (see above)
- Presigned upload requires MLflow 3.12+ server; older servers use proxy fallback only
- List returns one directory level at a time (matches MLflow REST API behavior)

### Neutral

- Proto bumped to MLflow v3.12.0 for `CreatePresignedUploadUrl`; unused v3.12 RPCs stripped during fetch to avoid import cycles

## References

- [MLflow REST API — Artifacts](https://mlflow.org/docs/latest/rest-api.html)
- [MLflow artifact proxy documentation](https://mlflow.org/docs/latest/tracking.html#using-the-tracking-server-exclusively-for-proxied-artifact-access)
- ADR-0005: Multi-Package Structure
- ADR-0009: Experiment Tracking Client (artifacts excluded from MVP)
