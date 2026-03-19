# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**ghfs** is a Go CLI that uses GitHub repositories as a distributed, encrypted storage backend. Files are split into 4MB content-addressed chunks, encrypted client-side with age, and stored as base64-encoded blobs across auto-created private GitHub repos. A local SQLite index tracks chunk-to-repo mappings, and an encrypted manifest in a dedicated repo is the canonical file registry.

## Build & Development

```bash
go build -o ghfs ./cmd/ghfs     # build binary
go test ./...                    # run all tests
go test ./pkg/chunk/...          # run single package tests
go test ./test/...               # run integration tests
go vet ./...                     # lint
```

## Architecture

### Package Dependency Graph

```
cmd/ghfs (CLI)  →  pkg/fs (orchestrator)
                     ├── pkg/chunk      (content-addressed splitting, no deps)
                     ├── pkg/crypto     (age encrypt/decrypt, no deps)
                     ├── pkg/github     (Contents API client, no deps)
                     ├── pkg/store      (SQLite index, no deps)
                     ├── pkg/volume     (repo rotation, depends on github + store)
                     ├── pkg/manifest   (encrypted manifest sync, depends on github + crypto)
                     ├── pkg/cache      (LRU disk cache for chunks, no deps)
                     └── pkg/fuse       (FUSE filesystem, depends on fs + store + cache + github + crypto)
```

### Key Interfaces (all defined in their respective packages)

- `github.ContentAPI` — PutFile/GetFile/DeleteFile/CreateRepo/RepoExists
- `store.Index` — chunk and file CRUD with ref-counting
- `volume.Allocator` — AllocateSlot/RecordChunk/ReleaseChunk
- `manifest.ManifestSync` — Pull/Push encrypted manifest

The `fs.FS` orchestrator accepts all four interfaces, enabling full unit testing with mocks.

### Data Flow

**Put**: file → `chunk.Split` (4MB) → for each new chunk: `crypto.Encrypt` → `volume.AllocateSlot` → `github.PutFile` → `store.PutChunk` → `store.PutFile` → manifest push

**Get**: `store.GetFile` → for each chunk: `store.GetChunk` → `github.GetFile` → `crypto.Decrypt` → `chunk.Reassemble`

**Rm**: `store.DeleteFile` (returns orphaned chunks via ref counting) → for each orphan: `github.DeleteFile` → `store.DeleteChunk` → `volume.ReleaseChunk` → manifest push

### Data Model

- **Chunks**: SHA256(plaintext) → encrypted blob at `chunks/<chunkID>` in a volume repo
- **Volumes**: Private repos named `ghfs-vol-001`, `ghfs-vol-002`, etc. Max 1000 chunks each.
- **Manifest**: JSON encrypted with age, stored as `manifest.age` in the `ghfs-manifest` repo
- **Local index**: SQLite with tables: `chunks` (with ref_count), `files`, `volumes`

### Key Constraints

- GitHub API rate limit: 5,000 req/hr authenticated — throughput ceiling
- Do NOT use Git LFS (hard bandwidth/storage limits)
- Contents API: ~25MB per PUT, GetFile falls back to Blobs API for files >1MB
- All encryption is client-side via age (passphrase-based, scrypt KDF)
- Retry with exponential backoff on 429/5xx (`pkg/github/retry.go`)

## CLI Commands

- `ghfs init` — create manifest repo, first volume, initialize SQLite (idempotent)
- `ghfs put <file> [virtual-path]` — upload with progress reporting
- `ghfs get <virtual-path> [output-path]` — download with progress reporting
- `ghfs ls [prefix]` — list files with size and date
- `ghfs rm <virtual-path>` — delete file and clean up orphaned chunks
- `ghfs config` — show configuration and database stats
- `ghfs mount <mountpoint>` — mount as read-write FUSE filesystem (foreground, Ctrl+C to unmount)
- `ghfs unmount <mountpoint>` — unmount a FUSE mount

Required env vars: `GHFS_TOKEN` (GitHub PAT), `GHFS_PASSPHRASE` (or `--passphrase` flag), `GHFS_OWNER` (or `--owner` flag)

All env vars can be set in a `.env` file (see `.env.example`).

## Testing

Integration tests in `test/` use a mock GitHub HTTP server (`httptest.NewServer`) with all real packages wired together. Unit tests in each package use interface mocks.

FUSE integration tests require the `fuse` build tag: `go test -tags fuse ./pkg/fuse/...`

### FUSE Architecture

The FUSE layer (`pkg/fuse/`) synthesizes a directory tree from flat virtual paths and delegates to the FS orchestrator:
- **Reads**: lazy-load chunks on first read (cache-first → GitHub fetch → decrypt → cache), hold reassembled bytes in handle
- **Writes**: buffer to temp file in `~/.ghfs/cache/staging/`, on close: chunk + encrypt + upload via `fs.Put`
- **Rename**: metadata-only (reuses content-addressed chunks, no re-upload)
- **Cache**: LRU disk cache at `~/.ghfs/cache/` keyed by chunk ID, configurable size cap (default 1GB)
