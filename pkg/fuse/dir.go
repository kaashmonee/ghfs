package ghfuse

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	gofuse "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Dir represents a directory in the ghfs FUSE filesystem.
type Dir struct {
	gofuse.Inode
	state    *mountState
	path     string   // virtual dir path like "/photos"
	treeNode *DirTree // pointer to corresponding DirTree node
}

// compile-time interface checks
var _ gofuse.InodeEmbedder = (*Dir)(nil)
var _ gofuse.NodeReaddirer = (*Dir)(nil)
var _ gofuse.NodeLookuper = (*Dir)(nil)
var _ gofuse.NodeCreater = (*Dir)(nil)
var _ gofuse.NodeMkdirer = (*Dir)(nil)
var _ gofuse.NodeUnlinker = (*Dir)(nil)
var _ gofuse.NodeRenamer = (*Dir)(nil)

// Readdir returns the contents of this directory as a DirStream.
func (d *Dir) Readdir(ctx context.Context) (gofuse.DirStream, syscall.Errno) {
	dirs, files := d.treeNode.ReadDir()

	entries := make([]fuse.DirEntry, 0, len(dirs)+len(files))
	for _, name := range dirs {
		entries = append(entries, fuse.DirEntry{
			Name: name,
			Mode: syscall.S_IFDIR,
		})
	}
	for _, name := range files {
		entries = append(entries, fuse.DirEntry{
			Name: name,
			Mode: syscall.S_IFREG,
		})
	}

	return gofuse.NewListDirStream(entries), 0
}

// Lookup finds a child of this directory by name.
func (d *Dir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gofuse.Inode, syscall.Errno) {
	d.treeNode.mu.RLock()
	childTree, isDir := d.treeNode.Children[name]
	virtualPath, isFile := d.treeNode.Files[name]
	d.treeNode.mu.RUnlock()

	if isDir {
		childPath := filepath.Join(d.path, name)
		child := &Dir{
			state:    d.state,
			path:     childPath,
			treeNode: childTree,
		}
		out.Mode = syscall.S_IFDIR | 0755
		inode := d.NewInode(ctx, child, gofuse.StableAttr{Mode: syscall.S_IFDIR})
		return inode, 0
	}

	if isFile {
		_, size, err := d.state.store.GetFile(virtualPath)
		if err != nil {
			return nil, syscall.EIO
		}
		child := &File{
			state:       d.state,
			virtualPath: virtualPath,
		}
		out.Mode = syscall.S_IFREG | 0644
		out.Size = uint64(size)
		inode := d.NewInode(ctx, child, gofuse.StableAttr{Mode: syscall.S_IFREG})
		return inode, 0
	}

	return nil, syscall.ENOENT
}

// Create creates a new file in this directory.
func (d *Dir) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*gofuse.Inode, gofuse.FileHandle, uint32, syscall.Errno) {
	virtualPath := strings.TrimPrefix(filepath.Join(d.path, name), "/")

	tmpFile, err := os.CreateTemp(d.state.cache.StagingDir(), "ghfs-create-*")
	if err != nil {
		return nil, nil, 0, syscall.EIO
	}

	child := &File{
		state:       d.state,
		virtualPath: virtualPath,
	}
	out.Mode = syscall.S_IFREG | 0644

	inode := d.NewInode(ctx, child, gofuse.StableAttr{Mode: syscall.S_IFREG})

	handle := &Handle{
		state:       d.state,
		virtualPath: virtualPath,
		tmpFile:     tmpFile,
		writable:    true,
	}

	return inode, handle, 0, 0
}

// Mkdir creates a new subdirectory.
func (d *Dir) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*gofuse.Inode, syscall.Errno) {
	childTree := NewDirTree()

	d.treeNode.mu.Lock()
	d.treeNode.Children[name] = childTree
	d.treeNode.mu.Unlock()

	childPath := filepath.Join(d.path, name)
	child := &Dir{
		state:    d.state,
		path:     childPath,
		treeNode: childTree,
	}
	out.Mode = syscall.S_IFDIR | 0755

	inode := d.NewInode(ctx, child, gofuse.StableAttr{Mode: syscall.S_IFDIR})
	return inode, 0
}

// Unlink removes a file from this directory.
func (d *Dir) Unlink(ctx context.Context, name string) syscall.Errno {
	d.treeNode.mu.RLock()
	virtualPath, ok := d.treeNode.Files[name]
	d.treeNode.mu.RUnlock()

	if !ok {
		return syscall.ENOENT
	}

	if err := d.state.ghfs.Rm(ctx, virtualPath); err != nil {
		return syscall.EIO
	}

	d.state.tree.RemoveFile(virtualPath)
	return 0
}

// Rename moves a file or directory from this directory to another.
func (d *Dir) Rename(ctx context.Context, name string, newParent gofuse.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	newDir, ok := newParent.(*Dir)
	if !ok {
		return syscall.EINVAL
	}

	d.treeNode.mu.RLock()
	oldVirtualPath, isFile := d.treeNode.Files[name]
	d.treeNode.mu.RUnlock()

	if !isFile {
		return syscall.ENOENT
	}

	newVirtualPath := strings.TrimPrefix(filepath.Join(newDir.path, newName), "/")

	// Read old file metadata
	chunkIDs, size, err := d.state.store.GetFile(oldVirtualPath)
	if err != nil {
		return syscall.EIO
	}

	// Write new file record with same chunks
	if err := d.state.store.PutFile(newVirtualPath, chunkIDs, size); err != nil {
		return syscall.EIO
	}

	// Delete old file record (ignore orphaned chunks — same chunks are referenced by new file)
	if _, err := d.state.store.DeleteFile(oldVirtualPath); err != nil {
		// Best effort: log but the new record is already in place.
		fmt.Fprintf(os.Stderr, "ghfuse: rename cleanup error: %v\n", err)
	}

	// Update the tree
	d.state.tree.RemoveFile(oldVirtualPath)
	d.state.tree.AddFile(newVirtualPath)

	return 0
}
