// Package cache provides an LRU disk cache for content-addressed chunks.
// Cached chunks are stored as files named by their chunk ID (SHA256 hex)
// in a flat directory. Eviction removes the least-recently-used entries
// when the total cached size exceeds maxBytes.
package cache

import (
	"container/list"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// cacheEntry tracks a single cached chunk in the LRU list.
type cacheEntry struct {
	chunkID string
	size    int64
}

// Cache is a concurrency-safe LRU disk cache for chunk data.
type Cache struct {
	dir      string
	maxBytes int64
	curBytes int64
	items    *list.List
	index    map[string]*list.Element
	mu       sync.Mutex
}

// New creates a Cache rooted at dir with the given capacity in bytes.
// It creates dir and a staging/ subdirectory if they do not exist, then
// scans existing files to rebuild the LRU index (oldest mtime at back).
func New(dir string, maxBytes int64) (*Cache, error) {
	if err := os.MkdirAll(filepath.Join(dir, "staging"), 0o755); err != nil {
		return nil, err
	}

	c := &Cache{
		dir:      dir,
		maxBytes: maxBytes,
		items:    list.New(),
		index:    make(map[string]*list.Element),
	}

	if err := c.scan(); err != nil {
		return nil, err
	}

	return c, nil
}

// scan reads existing chunk files from the cache directory and rebuilds
// the LRU list ordered by modification time (most recent at front).
func (c *Cache) scan() error {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}

	type fileInfo struct {
		name    string
		size    int64
		modTime int64 // UnixNano
	}

	var files []fileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{
			name:    e.Name(),
			size:    info.Size(),
			modTime: info.ModTime().UnixNano(),
		})
	}

	// Sort by mtime ascending so we can push each to front;
	// the last pushed (newest) ends up at front of the list.
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime < files[j].modTime
	})

	for _, f := range files {
		entry := &cacheEntry{chunkID: f.name, size: f.size}
		elem := c.items.PushFront(entry)
		c.index[f.name] = elem
		c.curBytes += f.size
	}

	return nil
}

// Get retrieves chunk data from the cache. If the chunk is cached it is
// promoted to the front of the LRU list and the data and true are returned.
// If not cached, nil and false are returned.
func (c *Cache) Get(chunkID string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.index[chunkID]
	if !ok {
		return nil, false
	}

	c.items.MoveToFront(elem)

	data, err := os.ReadFile(filepath.Join(c.dir, chunkID))
	if err != nil {
		// File disappeared from disk; remove from index.
		c.remove(elem)
		return nil, false
	}

	return data, true
}

// Put stores chunk data in the cache. If the chunk already exists it is
// promoted to the front of the LRU list without re-writing the file.
// After insertion, entries are evicted from the back until curBytes <= maxBytes.
func (c *Cache) Put(chunkID string, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Duplicate: just promote.
	if elem, ok := c.index[chunkID]; ok {
		c.items.MoveToFront(elem)
		return nil
	}

	// Write file to disk.
	if err := os.WriteFile(filepath.Join(c.dir, chunkID), data, 0o644); err != nil {
		return err
	}

	size := int64(len(data))
	entry := &cacheEntry{chunkID: chunkID, size: size}
	elem := c.items.PushFront(entry)
	c.index[chunkID] = elem
	c.curBytes += size

	// Evict from back while over capacity.
	for c.curBytes > c.maxBytes && c.items.Len() > 0 {
		c.remove(c.items.Back())
	}

	return nil
}

// StagingDir returns the path to the staging subdirectory, which callers
// can use for writing temporary files before atomically moving them.
func (c *Cache) StagingDir() string {
	return filepath.Join(c.dir, "staging")
}

// remove deletes an element from the LRU list, the index, and disk.
func (c *Cache) remove(elem *list.Element) {
	entry := c.items.Remove(elem).(*cacheEntry)
	delete(c.index, entry.chunkID)
	c.curBytes -= entry.size
	// Best-effort disk removal.
	os.Remove(filepath.Join(c.dir, entry.chunkID))
}
