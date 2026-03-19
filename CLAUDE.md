# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**ghfs** is a CLI tool (Go) that uses GitHub repositories as a distributed, encrypted storage backend. Files are split into content-addressed chunks, encrypted client-side, and stored as blobs across multiple GitHub repos. A local SQLite index tracks chunk-to-repo mappings.

## Architecture

### Core Operations
- `put <file>` — chunk, encrypt, and push file to GitHub repo(s)
- `get <path>` — retrieve chunks, decrypt, and reassemble file
- `ls [path]` — list virtual filesystem contents
- `rm <path>` — remove chunks and update manifest

### Data Model
- **Chunks**: Content-addressed (SHA256 of plaintext) encrypted blobs stored as files in GitHub repos
- **Manifest**: Encrypted file mapping virtual paths → ordered list of chunk IDs
- **Volumes**: Each GitHub repo acts as a volume with a max chunk count; new repos are created when a volume fills
- **Local index**: SQLite database mapping chunk IDs → repo/path locations

### Key Constraints
- GitHub API rate limit: 5,000 requests/hour (authenticated) — this is the throughput ceiling
- Repos: soft 1GB recommendation, warnings at 5GB, no hard cap
- Do NOT use Git LFS (hard 1GB storage + 1GB/month bandwidth limits on free tier)
- Store data as regular git blobs, not LFS objects
- All encryption is client-side (age or NaCl)

## Build & Development

```bash
go build -o ghfs .
go test ./...
go test ./pkg/chunk/...    # run tests for a single package
go vet ./...
```

## Design Decisions
- Content-addressed chunking enables deduplication across files
- Client-side encryption means GitHub never sees plaintext
- Sharding across repos avoids per-repo size pressure
- Local SQLite index avoids extra API calls for lookups
- Base64 encoding for blobs stored as regular files in repos
