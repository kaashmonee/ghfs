package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPutGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir, 1<<20) // 1 MiB
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	data := []byte("hello world")
	if err := c.Put("abc123", data); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok := c.Get("abc123")
	if !ok {
		t.Fatal("Get returned false for cached chunk")
	}
	if string(got) != string(data) {
		t.Fatalf("Get data = %q; want %q", got, data)
	}
}

func TestGetNonexistent(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir, 1<<20)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, ok := c.Get("does-not-exist")
	if ok {
		t.Fatal("Get returned true for nonexistent chunk")
	}
	if got != nil {
		t.Fatalf("Get data = %v; want nil", got)
	}
}

func TestLRUEviction(t *testing.T) {
	dir := t.TempDir()
	// maxBytes = 20; each item is 10 bytes, so only 2 fit.
	c, err := New(dir, 20)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := c.Put("aaa", make([]byte, 10)); err != nil {
		t.Fatalf("Put aaa: %v", err)
	}
	if err := c.Put("bbb", make([]byte, 10)); err != nil {
		t.Fatalf("Put bbb: %v", err)
	}
	// This should evict "aaa" (oldest / back of list).
	if err := c.Put("ccc", make([]byte, 10)); err != nil {
		t.Fatalf("Put ccc: %v", err)
	}

	if _, ok := c.Get("aaa"); ok {
		t.Fatal("expected aaa to be evicted")
	}
	if _, ok := c.Get("bbb"); !ok {
		t.Fatal("expected bbb to still be cached")
	}
	if _, ok := c.Get("ccc"); !ok {
		t.Fatal("expected ccc to still be cached")
	}

	// Verify aaa file is removed from disk.
	if _, err := os.Stat(filepath.Join(dir, "aaa")); !os.IsNotExist(err) {
		t.Fatal("expected aaa file to be removed from disk")
	}
}

func TestPutDuplicateNoop(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir, 100)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	data := []byte("0123456789") // 10 bytes
	if err := c.Put("dup", data); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Put again — should not double-count.
	if err := c.Put("dup", data); err != nil {
		t.Fatalf("Put duplicate: %v", err)
	}

	c.mu.Lock()
	cur := c.curBytes
	count := len(c.index)
	c.mu.Unlock()

	if cur != 10 {
		t.Fatalf("curBytes = %d; want 10 (no double-count)", cur)
	}
	if count != 1 {
		t.Fatalf("index length = %d; want 1", count)
	}
}

func TestStartupScan(t *testing.T) {
	dir := t.TempDir()

	// Pre-create files with different mtimes.
	names := []string{"file1", "file2", "file3"}
	for i, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
		// Stagger mtimes so ordering is deterministic.
		mtime := time.Now().Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(filepath.Join(dir, name), mtime, mtime); err != nil {
			t.Fatalf("Chtimes %s: %v", name, err)
		}
	}

	c, err := New(dir, 1<<20)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, name := range names {
		if _, ok := c.Get(name); !ok {
			t.Fatalf("expected %s to be indexed after scan", name)
		}
	}

	c.mu.Lock()
	count := len(c.index)
	c.mu.Unlock()

	if count != 3 {
		t.Fatalf("index has %d entries; want 3", count)
	}
}

func TestStagingDirExists(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir, 1<<20)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	staging := c.StagingDir()
	expected := filepath.Join(dir, "staging")
	if staging != expected {
		t.Fatalf("StagingDir = %q; want %q", staging, expected)
	}

	info, err := os.Stat(staging)
	if err != nil {
		t.Fatalf("Stat staging dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("staging path is not a directory")
	}
}
