package ghfuse

import (
	"bytes"
	"context"
	"os"
	"syscall"

	gofuse "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/berthadev/ghfs/pkg/chunk"
	"github.com/berthadev/ghfs/pkg/crypto"
)

// Handle is a FileHandle for open files in the ghfs FUSE filesystem.
// It supports two modes: read (for existing files) and write (for newly created files).
type Handle struct {
	state       *mountState
	virtualPath string
	data        []byte   // cached file data for read mode
	tmpFile     *os.File // temporary file for write mode
	writable    bool
	loaded      bool
}

// compile-time interface checks
var _ gofuse.FileHandle = (*Handle)(nil)
var _ gofuse.FileReader = (*Handle)(nil)
var _ gofuse.FileWriter = (*Handle)(nil)
var _ gofuse.FileFlusher = (*Handle)(nil)
var _ gofuse.FileReleaser = (*Handle)(nil)

// Read loads the file data on first call, then serves the requested byte range.
func (h *Handle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if !h.loaded {
		if err := h.loadData(ctx); err != nil {
			return nil, syscall.EIO
		}
		h.loaded = true
	}

	dataLen := int64(len(h.data))
	if off >= dataLen {
		return fuse.ReadResultData(nil), 0
	}

	end := off + int64(len(dest))
	if end > dataLen {
		end = dataLen
	}

	return fuse.ReadResultData(h.data[off:end]), 0
}

// loadData fetches all chunks for the file, decrypting and caching as needed,
// then reassembles them into h.data.
func (h *Handle) loadData(ctx context.Context) error {
	chunkIDs, _, err := h.state.store.GetFile(h.virtualPath)
	if err != nil {
		return err
	}

	chunks := make([]chunk.Chunk, len(chunkIDs))
	for i, cid := range chunkIDs {
		// Try cache first.
		data, ok := h.state.cache.Get(cid)
		if !ok {
			// Cache miss: fetch from GitHub.
			repo, path, err := h.state.store.GetChunk(cid)
			if err != nil {
				return err
			}

			encrypted, err := h.state.client.GetFile(ctx, repo, path)
			if err != nil {
				return err
			}

			decrypted, err := crypto.Decrypt(encrypted, h.state.passphrase)
			if err != nil {
				return err
			}

			// Cache the decrypted chunk.
			if err := h.state.cache.Put(cid, decrypted); err != nil {
				return err
			}

			data = decrypted
		}

		chunks[i] = chunk.Chunk{
			ID:    cid,
			Data:  data,
			Index: i,
		}
	}

	var buf bytes.Buffer
	if err := chunk.Reassemble(chunks, &buf); err != nil {
		return err
	}

	h.data = buf.Bytes()
	return nil
}

// Write writes data to the temporary file at the given offset (write mode).
func (h *Handle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	if h.tmpFile == nil {
		return 0, syscall.EIO
	}

	if _, err := h.tmpFile.Seek(off, 0); err != nil {
		return 0, syscall.EIO
	}

	n, err := h.tmpFile.Write(data)
	if err != nil {
		return 0, syscall.EIO
	}

	return uint32(n), 0
}

// Flush is called on close. For writable handles, it uploads the file via ghfs.Put.
func (h *Handle) Flush(ctx context.Context) syscall.Errno {
	if !h.writable || h.tmpFile == nil {
		return 0
	}

	tmpPath := h.tmpFile.Name()

	// Close the temp file before uploading so all data is flushed.
	if err := h.tmpFile.Close(); err != nil {
		return syscall.EIO
	}
	h.tmpFile = nil

	if err := h.state.ghfs.Put(ctx, tmpPath, h.virtualPath); err != nil {
		return syscall.EIO
	}

	h.state.tree.AddFile(h.virtualPath)

	os.Remove(tmpPath)

	return 0
}

// Release cleans up the handle. For writable handles, removes the temp file if it still exists.
func (h *Handle) Release(ctx context.Context) syscall.Errno {
	if h.tmpFile != nil {
		tmpPath := h.tmpFile.Name()
		h.tmpFile.Close()
		h.tmpFile = nil
		os.Remove(tmpPath)
	}
	h.data = nil
	return 0
}
