package ghfuse

import (
	"context"
	"syscall"

	gofuse "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// File represents a regular file in the ghfs FUSE filesystem.
type File struct {
	gofuse.Inode
	state       *mountState
	virtualPath string
}

// compile-time interface checks
var _ gofuse.InodeEmbedder = (*File)(nil)
var _ gofuse.NodeGetattrer = (*File)(nil)
var _ gofuse.NodeOpener = (*File)(nil)

// Getattr returns file attributes.
func (f *File) Getattr(ctx context.Context, fh gofuse.FileHandle, out *fuse.AttrOut) syscall.Errno {
	_, size, err := f.state.store.GetFile(f.virtualPath)
	if err != nil {
		return syscall.EIO
	}
	out.Size = uint64(size)
	out.Mode = syscall.S_IFREG | 0644
	return 0
}

// Open opens the file for reading and returns a FileHandle.
func (f *File) Open(ctx context.Context, flags uint32) (gofuse.FileHandle, uint32, syscall.Errno) {
	handle := &Handle{
		state:       f.state,
		virtualPath: f.virtualPath,
		writable:    false,
	}
	return handle, 0, 0
}
