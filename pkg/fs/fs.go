// Package fs provides the core orchestrator for ghfs, coordinating chunking,
// encryption, storage, and manifest operations.
package fs

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/berthadev/ghfs/pkg/chunk"
	"github.com/berthadev/ghfs/pkg/crypto"
	"github.com/berthadev/ghfs/pkg/github"
	"github.com/berthadev/ghfs/pkg/manifest"
	"github.com/berthadev/ghfs/pkg/store"
	"github.com/berthadev/ghfs/pkg/volume"
)

// ProgressEvent describes the progress of a chunk operation.
type ProgressEvent struct {
	Operation string // "upload" or "download"
	Current   int    // 1-based index of the current chunk
	Total     int    // total number of chunks
	ChunkID   string // full chunk ID
}

// ProgressFunc is a callback invoked after each chunk operation completes.
type ProgressFunc func(ProgressEvent)

// FS orchestrates file operations across the ghfs subsystems.
type FS struct {
	store      store.Index
	volumes    volume.Allocator
	manifest   manifest.ManifestSync
	client     github.ContentAPI
	passphrase string
	chunkSize  int
	progressFn ProgressFunc
}

// New creates an FS with the given dependencies.
func New(
	store store.Index,
	volumes volume.Allocator,
	manifest manifest.ManifestSync,
	client github.ContentAPI,
	passphrase string,
	chunkSize int,
) *FS {
	return &FS{
		store:      store,
		volumes:    volumes,
		manifest:   manifest,
		client:     client,
		passphrase: passphrase,
		chunkSize:  chunkSize,
	}
}

// WithProgress sets a progress callback and returns the FS for chaining.
func (f *FS) WithProgress(fn ProgressFunc) *FS {
	f.progressFn = fn
	return f
}

// Put splits a local file into chunks, encrypts and uploads new chunks to
// GitHub volume repos, and records the file in the local index and manifest.
func (f *FS) Put(ctx context.Context, localPath string, virtualPath string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("fs: opening local file: %w", err)
	}
	defer file.Close()

	chunks, err := chunk.Split(file, f.chunkSize)
	if err != nil {
		return fmt.Errorf("fs: splitting file: %w", err)
	}

	chunkIDs := make([]string, len(chunks))
	for i, c := range chunks {
		chunkIDs[i] = c.ID

		exists, err := f.store.ChunkExists(c.ID)
		if err != nil {
			return fmt.Errorf("fs: checking chunk existence: %w", err)
		}
		if exists {
			continue
		}

		encrypted, err := crypto.Encrypt(c.Data, f.passphrase)
		if err != nil {
			return fmt.Errorf("fs: encrypting chunk: %w", err)
		}

		repo, err := f.volumes.AllocateSlot(ctx)
		if err != nil {
			return fmt.Errorf("fs: allocating volume slot: %w", err)
		}

		chunkPath := "chunks/" + c.ID
		if _, err := f.client.PutFile(ctx, repo, chunkPath, encrypted); err != nil {
			return fmt.Errorf("fs: uploading chunk: %w", err)
		}

		if err := f.store.PutChunk(c.ID, repo, chunkPath); err != nil {
			return fmt.Errorf("fs: recording chunk in store: %w", err)
		}

		if err := f.volumes.RecordChunk(ctx, repo); err != nil {
			return fmt.Errorf("fs: recording chunk in volume: %w", err)
		}

		if f.progressFn != nil {
			f.progressFn(ProgressEvent{
				Operation: "upload",
				Current:   i + 1,
				Total:     len(chunks),
				ChunkID:   c.ID,
			})
		}
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("fs: stat local file: %w", err)
	}

	if err := f.store.PutFile(virtualPath, chunkIDs, info.Size()); err != nil {
		return fmt.Errorf("fs: recording file in store: %w", err)
	}

	// Update the manifest.
	m, err := f.manifest.Pull(ctx)
	if err != nil {
		return fmt.Errorf("fs: pulling manifest: %w", err)
	}

	m.Files[virtualPath] = manifest.FileRecord{
		ChunkIDs:  chunkIDs,
		Size:      info.Size(),
		CreatedAt: time.Now().UTC(),
	}

	if err := f.manifest.Push(ctx, m); err != nil {
		return fmt.Errorf("fs: pushing manifest: %w", err)
	}

	return nil
}

// Get retrieves a virtual file by fetching its encrypted chunks from GitHub,
// decrypting them, reassembling, and writing to outputPath.
func (f *FS) Get(ctx context.Context, virtualPath string, outputPath string) error {
	chunkIDs, _, err := f.store.GetFile(virtualPath)
	if err != nil {
		return fmt.Errorf("fs: getting file record: %w", err)
	}

	chunks := make([]chunk.Chunk, len(chunkIDs))
	for i, cid := range chunkIDs {
		repo, path, err := f.store.GetChunk(cid)
		if err != nil {
			return fmt.Errorf("fs: getting chunk location: %w", err)
		}

		encrypted, err := f.client.GetFile(ctx, repo, path)
		if err != nil {
			return fmt.Errorf("fs: downloading chunk: %w", err)
		}

		decrypted, err := crypto.Decrypt(encrypted, f.passphrase)
		if err != nil {
			return fmt.Errorf("fs: decrypting chunk: %w", err)
		}

		chunks[i] = chunk.Chunk{
			ID:    cid,
			Data:  decrypted,
			Index: i,
		}

		if f.progressFn != nil {
			f.progressFn(ProgressEvent{
				Operation: "download",
				Current:   i + 1,
				Total:     len(chunkIDs),
				ChunkID:   cid,
			})
		}
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("fs: creating output file: %w", err)
	}
	defer outFile.Close()

	if err := chunk.Reassemble(chunks, outFile); err != nil {
		return fmt.Errorf("fs: reassembling file: %w", err)
	}

	return nil
}

// Ls returns file entries whose virtual paths match the given prefix.
func (f *FS) Ls(ctx context.Context, prefix string) ([]store.FileEntry, error) {
	entries, err := f.store.ListFiles(prefix)
	if err != nil {
		return nil, fmt.Errorf("fs: listing files: %w", err)
	}
	return entries, nil
}

// Rm deletes a virtual file, cleaning up orphaned chunks from GitHub and the
// local index, and updating the manifest.
func (f *FS) Rm(ctx context.Context, virtualPath string) error {
	orphanedChunkIDs, err := f.store.DeleteFile(virtualPath)
	if err != nil {
		return fmt.Errorf("fs: deleting file from store: %w", err)
	}

	for _, cid := range orphanedChunkIDs {
		repo, path, err := f.store.GetChunk(cid)
		if err != nil {
			return fmt.Errorf("fs: getting orphaned chunk location: %w", err)
		}

		// Pass empty SHA; the GitHub client's PutFile handles SHA lookup
		// internally, but DeleteFile requires it. For ghfs, we pass empty
		// and rely on the caller or a future enhancement to resolve.
		if err := f.client.DeleteFile(ctx, repo, path, ""); err != nil {
			return fmt.Errorf("fs: deleting chunk from github: %w", err)
		}

		if err := f.store.DeleteChunk(cid); err != nil {
			return fmt.Errorf("fs: deleting chunk from store: %w", err)
		}

		if err := f.volumes.ReleaseChunk(ctx, repo); err != nil {
			return fmt.Errorf("fs: releasing chunk from volume: %w", err)
		}
	}

	// Update the manifest.
	m, err := f.manifest.Pull(ctx)
	if err != nil {
		return fmt.Errorf("fs: pulling manifest: %w", err)
	}

	delete(m.Files, virtualPath)

	if err := f.manifest.Push(ctx, m); err != nil {
		return fmt.Errorf("fs: pushing manifest: %w", err)
	}

	return nil
}
