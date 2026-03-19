# ghfs

A CLI tool that uses GitHub repositories as a distributed, encrypted storage backend. Files are split into content-addressed chunks, encrypted client-side with [age](https://github.com/FiloSottile/age), and stored as blobs across multiple private GitHub repos.

## How it works

```
local file → 4MB chunks → SHA256 content-addressing → age encryption → base64 → GitHub repos
```

- **Chunking**: Files are split into 4MB content-addressed chunks (SHA256). Identical chunks are deduplicated automatically.
- **Encryption**: Each chunk is encrypted client-side using age with passphrase-based key derivation. GitHub never sees plaintext.
- **Storage**: Encrypted chunks are stored as regular files in private GitHub repos ("volumes"). When a volume fills up (1000 chunks / ~4GB), a new one is auto-created.
- **Index**: A local SQLite database tracks chunk locations. An encrypted manifest in a dedicated GitHub repo provides the canonical file registry.
- **FUSE**: Mount your stored files as a regular filesystem. Read, write, rename, and delete files using any program.

## Setup

```bash
go build -o ghfs ./cmd/ghfs
```

Create a `.env` file (see `.env.example`):

```
GHFS_TOKEN=ghp_your_github_personal_access_token
GHFS_PASSPHRASE=your-secret-passphrase
GHFS_OWNER=your-github-username
```

Initialize (creates GitHub repos and local database):

```bash
./ghfs init
```

## Usage

### CLI

```bash
# Upload a file
./ghfs put photo.jpg

# Upload with a custom virtual path
./ghfs put photo.jpg /photos/vacation/beach.jpg

# List stored files
./ghfs ls
./ghfs ls /photos/

# Download a file
./ghfs get /photos/vacation/beach.jpg output.jpg

# Delete a file
./ghfs rm /photos/vacation/beach.jpg

# Show config and stats
./ghfs config
```

### FUSE Mount

Mount your stored files as a regular filesystem:

```bash
# Mount (foreground, Ctrl+C to unmount)
./ghfs mount ~/ghfs-mount

# Mount with custom cache settings
./ghfs mount ~/ghfs-mount --cache-dir /tmp/ghfs-cache --cache-size 2147483648

# Unmount
./ghfs unmount ~/ghfs-mount
```

Once mounted, use any program to interact with your files:

```bash
ls ~/ghfs-mount
cat ~/ghfs-mount/photo.jpg
cp newfile.txt ~/ghfs-mount/
rm ~/ghfs-mount/old.txt
```

## Architecture

```
cmd/ghfs/         CLI (cobra)
pkg/fs/           Orchestrator (Put/Get/Ls/Rm)
pkg/chunk/        Content-addressed 4MB chunking
pkg/crypto/       age encryption/decryption
pkg/github/       GitHub Contents API client
pkg/store/        SQLite local index with ref-counting
pkg/volume/       Auto-create and rotate volume repos
pkg/manifest/     Encrypted manifest sync to GitHub
pkg/cache/        LRU disk cache for FUSE reads
pkg/fuse/         FUSE filesystem (read-write)
pkg/envfile/      .env file loader
```

## Constraints

- GitHub API rate limit: 5,000 requests/hour (authenticated) — this is your throughput ceiling
- Does not use Git LFS (hard bandwidth/storage limits on free tier)
- Single-device (the local SQLite index is not synced)
- This is a proof-of-concept. GitHub's ToS prohibits using repos as general-purpose storage. Use at your own risk.

## Testing

```bash
go test ./...                                    # all unit + integration tests
go test -tags fuse ./pkg/fuse/...                # FUSE integration tests (requires /dev/fuse)
```
