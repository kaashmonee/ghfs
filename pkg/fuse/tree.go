// Package ghfuse provides directory tree synthesis for the ghfs FUSE layer.
package ghfuse

import (
	"sort"
	"strings"
	"sync"

	"github.com/berthadev/ghfs/pkg/store"
)

// DirTree represents a node in a virtual directory tree. Each node holds
// child directories and files that exist at this level of the hierarchy.
type DirTree struct {
	Children map[string]*DirTree // subdirectory name -> subtree
	Files    map[string]string   // filename -> full virtual path
	mu       sync.RWMutex
}

// NewDirTree returns a DirTree initialized with empty maps.
func NewDirTree() *DirTree {
	return &DirTree{
		Children: make(map[string]*DirTree),
		Files:    make(map[string]string),
	}
}

// BuildTree constructs a DirTree from a slice of FileEntry records. Each
// entry's Path is split by "/" and intermediate directory nodes are created
// as needed. Leading "/" characters are stripped from paths.
func BuildTree(entries []store.FileEntry) *DirTree {
	root := NewDirTree()
	for _, e := range entries {
		p := strings.TrimPrefix(e.Path, "/")
		if p == "" {
			continue
		}
		parts := strings.Split(p, "/")
		node := root
		// Walk/create intermediate directories.
		for _, dir := range parts[:len(parts)-1] {
			child, ok := node.Children[dir]
			if !ok {
				child = NewDirTree()
				node.Children[dir] = child
			}
			node = child
		}
		// Add the leaf file to the final directory's Files map.
		filename := parts[len(parts)-1]
		node.Files[filename] = p
	}
	return root
}

// Lookup traverses Children by the given path parts and returns the subtree
// at that location. It returns false if any intermediate directory is missing.
func (d *DirTree) Lookup(parts []string) (*DirTree, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	node := d
	for _, part := range parts {
		child, ok := node.Children[part]
		if !ok {
			return nil, false
		}
		node = child
	}
	return node, true
}

// LookupFile traverses to the parent directory of the path described by parts,
// then checks for the last part in the Files map. If found it returns the full
// virtual path stored for that file.
func (d *DirTree) LookupFile(parts []string) (string, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if len(parts) == 0 {
		return "", false
	}

	node := d
	for _, dir := range parts[:len(parts)-1] {
		child, ok := node.Children[dir]
		if !ok {
			return "", false
		}
		node = child
	}

	vp, ok := node.Files[parts[len(parts)-1]]
	return vp, ok
}

// AddFile adds a file to the tree at the given virtual path, creating
// intermediate directory nodes as needed. It takes a write lock on the root.
func (d *DirTree) AddFile(virtualPath string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	p := strings.TrimPrefix(virtualPath, "/")
	if p == "" {
		return
	}
	parts := strings.Split(p, "/")

	node := d
	for _, dir := range parts[:len(parts)-1] {
		child, ok := node.Children[dir]
		if !ok {
			child = NewDirTree()
			node.Children[dir] = child
		}
		node = child
	}
	filename := parts[len(parts)-1]
	node.Files[filename] = p
}

// RemoveFile removes a file from the tree at the given virtual path.
// Empty parent directories are cleaned up after removal. It takes a write
// lock on the root.
func (d *DirTree) RemoveFile(virtualPath string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	p := strings.TrimPrefix(virtualPath, "/")
	if p == "" {
		return
	}
	parts := strings.Split(p, "/")

	// Collect the chain of (parent, childName) pairs so we can clean up.
	type ancestor struct {
		node     *DirTree
		childKey string
	}
	var chain []ancestor

	node := d
	for _, dir := range parts[:len(parts)-1] {
		child, ok := node.Children[dir]
		if !ok {
			return // path does not exist
		}
		chain = append(chain, ancestor{node: node, childKey: dir})
		node = child
	}

	filename := parts[len(parts)-1]
	if _, ok := node.Files[filename]; !ok {
		return // file not found
	}
	delete(node.Files, filename)

	// Walk backwards cleaning up empty directories.
	for i := len(chain) - 1; i >= 0; i-- {
		a := chain[i]
		child := a.node.Children[a.childKey]
		if len(child.Children) == 0 && len(child.Files) == 0 {
			delete(a.node.Children, a.childKey)
		} else {
			break
		}
	}
}

// ReadDir returns sorted lists of child directory names and file names at
// this node. It takes a read lock.
func (d *DirTree) ReadDir() (dirs []string, files []string) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	dirs = make([]string, 0, len(d.Children))
	for name := range d.Children {
		dirs = append(dirs, name)
	}
	sort.Strings(dirs)

	files = make([]string, 0, len(d.Files))
	for name := range d.Files {
		files = append(files, name)
	}
	sort.Strings(files)

	return dirs, files
}
