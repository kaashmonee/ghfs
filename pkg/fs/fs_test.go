package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/berthadev/ghfs/pkg/manifest"
	"github.com/berthadev/ghfs/pkg/store"
)

// --- Mock implementations ---

// mockStore implements store.Index using in-memory maps.
type mockStore struct {
	mu     sync.Mutex
	chunks map[string]chunkRecord   // chunkID -> record
	files  map[string]fileRecord    // virtualPath -> record
}

type chunkRecord struct {
	repo     string
	path     string
	refCount int
}

type fileRecord struct {
	chunkIDs []string
	size     int64
}

func newMockStore() *mockStore {
	return &mockStore{
		chunks: make(map[string]chunkRecord),
		files:  make(map[string]fileRecord),
	}
}

func (m *mockStore) PutChunk(chunkID, repo, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.chunks[chunkID]; !ok {
		m.chunks[chunkID] = chunkRecord{repo: repo, path: path, refCount: 0}
	}
	return nil
}

func (m *mockStore) GetChunk(chunkID string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.chunks[chunkID]
	if !ok {
		return "", "", store.ErrNotFound
	}
	return rec.repo, rec.path, nil
}

func (m *mockStore) ChunkExists(chunkID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.chunks[chunkID]
	return ok, nil
}

func (m *mockStore) PutFile(virtualPath string, chunkIDs []string, size int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[virtualPath] = fileRecord{chunkIDs: chunkIDs, size: size}
	for _, cid := range chunkIDs {
		if rec, ok := m.chunks[cid]; ok {
			rec.refCount++
			m.chunks[cid] = rec
		}
	}
	return nil
}

func (m *mockStore) GetFile(virtualPath string) ([]string, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.files[virtualPath]
	if !ok {
		return nil, 0, store.ErrNotFound
	}
	ids := make([]string, len(rec.chunkIDs))
	copy(ids, rec.chunkIDs)
	return ids, rec.size, nil
}

func (m *mockStore) ListFiles(prefix string) ([]store.FileEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var entries []store.FileEntry
	for p, rec := range m.files {
		if len(p) >= len(prefix) && p[:len(prefix)] == prefix {
			entries = append(entries, store.FileEntry{
				Path: p,
				Size: rec.size,
			})
		}
	}
	return entries, nil
}

func (m *mockStore) DeleteFile(virtualPath string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.files[virtualPath]
	if !ok {
		return nil, store.ErrNotFound
	}
	delete(m.files, virtualPath)

	// Decrement ref counts and collect orphaned chunks.
	var orphaned []string
	for _, cid := range rec.chunkIDs {
		if cr, ok := m.chunks[cid]; ok {
			cr.refCount--
			m.chunks[cid] = cr
			if cr.refCount <= 0 {
				orphaned = append(orphaned, cid)
			}
		}
	}
	return orphaned, nil
}

func (m *mockStore) DeleteChunk(chunkID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.chunks, chunkID)
	return nil
}

func (m *mockStore) Close() error {
	return nil
}

// mockClient implements github.ContentAPI using in-memory maps.
type mockClient struct {
	mu       sync.Mutex
	files    map[string][]byte // "repo/path" -> content
	putCount int               // total PutFile calls
}

func newMockClient() *mockClient {
	return &mockClient{
		files: make(map[string][]byte),
	}
}

func (m *mockClient) PutFile(_ context.Context, repo, path string, content []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := repo + "/" + path
	data := make([]byte, len(content))
	copy(data, content)
	m.files[key] = data
	m.putCount++
	return "sha-" + key, nil
}

func (m *mockClient) GetFile(_ context.Context, repo, path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := repo + "/" + path
	data, ok := m.files[key]
	if !ok {
		return nil, fmt.Errorf("mock: file not found: %s", key)
	}
	result := make([]byte, len(data))
	copy(result, data)
	return result, nil
}

func (m *mockClient) DeleteFile(_ context.Context, repo, path, sha string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := repo + "/" + path
	delete(m.files, key)
	return nil
}

func (m *mockClient) CreateRepo(_ context.Context, name string, private bool) error {
	return nil
}

func (m *mockClient) RepoExists(_ context.Context, name string) (bool, error) {
	return true, nil
}

func (m *mockClient) getPutCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.putCount
}

func (m *mockClient) fileCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.files)
}

// mockAllocator implements volume.Allocator returning a fixed repo name.
type mockAllocator struct {
	mu         sync.Mutex
	repo       string
	chunkCount int
}

func newMockAllocator(repo string) *mockAllocator {
	return &mockAllocator{repo: repo}
}

func (m *mockAllocator) AllocateSlot(_ context.Context) (string, error) {
	return m.repo, nil
}

func (m *mockAllocator) RecordChunk(_ context.Context, repo string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chunkCount++
	return nil
}

func (m *mockAllocator) ReleaseChunk(_ context.Context, repo string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chunkCount--
	return nil
}

// mockManifest implements manifest.ManifestSync using an in-memory manifest.
type mockManifest struct {
	mu   sync.Mutex
	data []byte // JSON-encoded manifest
}

func newMockManifest() *mockManifest {
	m := manifest.NewManifest()
	data, _ := json.Marshal(m)
	return &mockManifest{data: data}
}

func (m *mockManifest) Pull(_ context.Context) (*manifest.Manifest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var man manifest.Manifest
	if err := json.Unmarshal(m.data, &man); err != nil {
		return nil, err
	}
	return &man, nil
}

func (m *mockManifest) Push(_ context.Context, man *manifest.Manifest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := json.Marshal(man)
	if err != nil {
		return err
	}
	m.data = data
	return nil
}

// --- Helper ---

// createTestFile writes content to a temp file and returns its path.
func createTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// newTestFS creates an FS with mock dependencies and returns everything.
func newTestFS(t *testing.T) (*FS, *mockStore, *mockClient, *mockAllocator, *mockManifest) {
	t.Helper()
	s := newMockStore()
	c := newMockClient()
	a := newMockAllocator("test-vol-001")
	m := newMockManifest()
	// Use a simple passphrase and small chunk size for tests.
	f := New(s, a, m, c, "test-passphrase", 64)
	return f, s, c, a, m
}

// --- Tests ---

func TestPutGetRoundTrip(t *testing.T) {
	fsys, _, _, _, _ := newTestFS(t)
	ctx := context.Background()
	dir := t.TempDir()

	content := "Hello, ghfs! This is a test file for round-trip verification."
	localPath := createTestFile(t, dir, "input.txt", content)
	outputPath := filepath.Join(dir, "output.txt")

	if err := fsys.Put(ctx, localPath, "/docs/test.txt"); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if err := fsys.Get(ctx, "/docs/test.txt", outputPath); err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}

	if string(got) != content {
		t.Fatalf("round-trip mismatch:\n  want: %q\n  got:  %q", content, string(got))
	}
}

func TestDedup(t *testing.T) {
	fsys, _, client, _, _ := newTestFS(t)
	ctx := context.Background()
	dir := t.TempDir()

	content := "duplicate content for dedup test"
	path1 := createTestFile(t, dir, "file1.txt", content)
	path2 := createTestFile(t, dir, "file2.txt", content)

	if err := fsys.Put(ctx, path1, "/a.txt"); err != nil {
		t.Fatalf("Put 1 failed: %v", err)
	}

	putCountAfterFirst := client.getPutCount()

	if err := fsys.Put(ctx, path2, "/b.txt"); err != nil {
		t.Fatalf("Put 2 failed: %v", err)
	}

	putCountAfterSecond := client.getPutCount()

	// The second Put should NOT have uploaded any new chunks to GitHub,
	// but it still calls manifest Push, which uses PutFile on the manifest repo.
	// Our mock manifest does NOT use the client, so putCount should be the same.
	if putCountAfterSecond != putCountAfterFirst {
		t.Fatalf("dedup failed: expected %d PutFile calls after second Put, got %d",
			putCountAfterFirst, putCountAfterSecond)
	}
}

func TestLs(t *testing.T) {
	fsys, _, _, _, _ := newTestFS(t)
	ctx := context.Background()
	dir := t.TempDir()

	createTestFile(t, dir, "a.txt", "aaa")
	createTestFile(t, dir, "b.txt", "bbb")

	if err := fsys.Put(ctx, filepath.Join(dir, "a.txt"), "/files/a.txt"); err != nil {
		t.Fatalf("Put a failed: %v", err)
	}
	if err := fsys.Put(ctx, filepath.Join(dir, "b.txt"), "/files/b.txt"); err != nil {
		t.Fatalf("Put b failed: %v", err)
	}

	entries, err := fsys.Ls(ctx, "/files/")
	if err != nil {
		t.Fatalf("Ls failed: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	paths := make(map[string]bool)
	for _, e := range entries {
		paths[e.Path] = true
	}
	if !paths["/files/a.txt"] || !paths["/files/b.txt"] {
		t.Fatalf("unexpected entries: %v", entries)
	}
}

func TestRm(t *testing.T) {
	fsys, s, client, _, _ := newTestFS(t)
	ctx := context.Background()
	dir := t.TempDir()

	content := "file to remove"
	localPath := createTestFile(t, dir, "rm.txt", content)

	if err := fsys.Put(ctx, localPath, "/rm/test.txt"); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Verify file exists.
	entries, err := fsys.Ls(ctx, "/rm/")
	if err != nil {
		t.Fatalf("Ls failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry before Rm, got %d", len(entries))
	}

	// Record chunk count in client before Rm.
	chunkFilesBefore := client.fileCount()
	if chunkFilesBefore == 0 {
		t.Fatal("expected chunks in client before Rm")
	}

	if err := fsys.Rm(ctx, "/rm/test.txt"); err != nil {
		t.Fatalf("Rm failed: %v", err)
	}

	// Verify file is gone.
	entries, err = fsys.Ls(ctx, "/rm/")
	if err != nil {
		t.Fatalf("Ls after Rm failed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after Rm, got %d", len(entries))
	}

	// Verify orphaned chunks were cleaned up from client.
	chunkFilesAfter := client.fileCount()
	if chunkFilesAfter != 0 {
		t.Fatalf("expected 0 chunk files in client after Rm, got %d", chunkFilesAfter)
	}

	// Verify chunks removed from store.
	s.mu.Lock()
	chunkCount := len(s.chunks)
	s.mu.Unlock()
	if chunkCount != 0 {
		t.Fatalf("expected 0 chunks in store after Rm, got %d", chunkCount)
	}
}

func TestRmSharedChunks(t *testing.T) {
	fsys, s, client, _, _ := newTestFS(t)
	ctx := context.Background()
	dir := t.TempDir()

	// Two identical files -> same chunks.
	content := "shared chunk content for testing"
	path1 := createTestFile(t, dir, "shared1.txt", content)
	path2 := createTestFile(t, dir, "shared2.txt", content)

	if err := fsys.Put(ctx, path1, "/shared/a.txt"); err != nil {
		t.Fatalf("Put 1 failed: %v", err)
	}
	if err := fsys.Put(ctx, path2, "/shared/b.txt"); err != nil {
		t.Fatalf("Put 2 failed: %v", err)
	}

	// Remove first file -- chunks should NOT be deleted (still referenced).
	if err := fsys.Rm(ctx, "/shared/a.txt"); err != nil {
		t.Fatalf("Rm a failed: %v", err)
	}

	// Chunks should still exist in both client and store.
	s.mu.Lock()
	chunkCount := len(s.chunks)
	s.mu.Unlock()
	if chunkCount == 0 {
		t.Fatal("chunks should still exist after removing first file (shared)")
	}
	if client.fileCount() == 0 {
		t.Fatal("client should still have chunk files after removing first file (shared)")
	}

	// Remove second file -- now chunks should be orphaned and deleted.
	if err := fsys.Rm(ctx, "/shared/b.txt"); err != nil {
		t.Fatalf("Rm b failed: %v", err)
	}

	s.mu.Lock()
	chunkCount = len(s.chunks)
	s.mu.Unlock()
	if chunkCount != 0 {
		t.Fatalf("expected 0 chunks after removing both files, got %d", chunkCount)
	}
	if client.fileCount() != 0 {
		t.Fatalf("expected 0 client files after removing both files, got %d", client.fileCount())
	}
}
