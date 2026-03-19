package ghfuse

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/berthadev/ghfs/pkg/cache"
	"github.com/berthadev/ghfs/pkg/crypto"
	"github.com/berthadev/ghfs/pkg/store"
)

// --- Mocks ---

type mockIndex struct {
	mu     sync.Mutex
	chunks map[string]mockChunkRec
	files  map[string]mockFileRec
}

type mockChunkRec struct {
	repo, path string
	refCount   int
}

type mockFileRec struct {
	chunkIDs []string
	size     int64
}

func newMockIndex() *mockIndex {
	return &mockIndex{
		chunks: make(map[string]mockChunkRec),
		files:  make(map[string]mockFileRec),
	}
}

func (m *mockIndex) PutChunk(chunkID, repo, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.chunks[chunkID]; !ok {
		m.chunks[chunkID] = mockChunkRec{repo: repo, path: path}
	}
	return nil
}

func (m *mockIndex) GetChunk(chunkID string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.chunks[chunkID]
	if !ok {
		return "", "", store.ErrNotFound
	}
	return rec.repo, rec.path, nil
}

func (m *mockIndex) ChunkExists(chunkID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.chunks[chunkID]
	return ok, nil
}

func (m *mockIndex) PutFile(virtualPath string, chunkIDs []string, size int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[virtualPath] = mockFileRec{chunkIDs: chunkIDs, size: size}
	for _, cid := range chunkIDs {
		if rec, ok := m.chunks[cid]; ok {
			rec.refCount++
			m.chunks[cid] = rec
		}
	}
	return nil
}

func (m *mockIndex) GetFile(virtualPath string) ([]string, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.files[virtualPath]
	if !ok {
		return nil, 0, store.ErrNotFound
	}
	return rec.chunkIDs, rec.size, nil
}

func (m *mockIndex) ListFiles(prefix string) ([]store.FileEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var entries []store.FileEntry
	for path, rec := range m.files {
		if prefix == "" || len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			entries = append(entries, store.FileEntry{Path: path, Size: rec.size})
		}
	}
	return entries, nil
}

func (m *mockIndex) DeleteFile(virtualPath string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.files[virtualPath]
	if !ok {
		return nil, store.ErrNotFound
	}
	delete(m.files, virtualPath)
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

func (m *mockIndex) DeleteChunk(chunkID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.chunks, chunkID)
	return nil
}

func (m *mockIndex) Close() error { return nil }

// mockClient implements github.ContentAPI with in-memory storage.
type mockClient struct {
	mu    sync.Mutex
	files map[string][]byte // "repo/path" -> content
	repos map[string]bool
}

func newMockClient() *mockClient {
	return &mockClient{
		files: make(map[string][]byte),
		repos: make(map[string]bool),
	}
}

func (m *mockClient) PutFile(ctx context.Context, repo, path string, content []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[repo+"/"+path] = content
	return "sha-fake", nil
}

func (m *mockClient) GetFile(ctx context.Context, repo, path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.files[repo+"/"+path]
	if !ok {
		return nil, store.ErrNotFound
	}
	return data, nil
}

func (m *mockClient) DeleteFile(ctx context.Context, repo, path, sha string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, repo+"/"+path)
	return nil
}

func (m *mockClient) CreateRepo(ctx context.Context, name string, private bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.repos[name] = true
	return nil
}

func (m *mockClient) RepoExists(ctx context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.repos[name], nil
}

// --- Helpers ---

const testPass = "test-pass-unit"

func setupUnitState(t *testing.T) (*mountState, *mockIndex, *mockClient) {
	t.Helper()
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache")
	c, err := cache.New(cacheDir, 50*1024*1024)
	if err != nil {
		t.Fatalf("creating cache: %v", err)
	}

	idx := newMockIndex()
	client := newMockClient()
	tree := NewDirTree()

	state := &mountState{
		ghfs:       nil, // only needed for Put/Rm which we test separately
		store:      idx,
		client:     client,
		cache:      c,
		tree:       tree,
		passphrase: testPass,
	}
	return state, idx, client
}

// storeEncryptedChunk encrypts data, stores it in the mock client and index,
// and returns the chunk ID used.
func storeEncryptedChunk(t *testing.T, state *mountState, idx *mockIndex, client *mockClient, chunkID, repo string, plaintext []byte) {
	t.Helper()
	encrypted, err := crypto.Encrypt(plaintext, testPass)
	if err != nil {
		t.Fatalf("encrypting chunk: %v", err)
	}
	path := "chunks/" + chunkID
	client.mu.Lock()
	client.files[repo+"/"+path] = encrypted
	client.mu.Unlock()

	idx.mu.Lock()
	idx.chunks[chunkID] = mockChunkRec{repo: repo, path: path, refCount: 1}
	idx.mu.Unlock()
}

// --- Handle Unit Tests ---

func TestHandle_Read_LoadsAndServesData(t *testing.T) {
	state, idx, client := setupUnitState(t)

	plaintext := []byte("hello world from handle test")
	chunkID := "aaaa1111bbbb2222"

	storeEncryptedChunk(t, state, idx, client, chunkID, "vol-001", plaintext)

	idx.mu.Lock()
	idx.files["test.txt"] = mockFileRec{chunkIDs: []string{chunkID}, size: int64(len(plaintext))}
	idx.mu.Unlock()

	h := &Handle{
		state:       state,
		virtualPath: "test.txt",
		writable:    false,
	}

	ctx := context.Background()
	dest := make([]byte, 1024)
	result, errno := h.Read(ctx, dest, 0)
	if errno != 0 {
		t.Fatalf("Read returned errno %d", errno)
	}

	buf := make([]byte, 1024)
	data, _ := result.Bytes(buf)
	if string(data) != string(plaintext) {
		t.Fatalf("Read content mismatch: got %q, want %q", string(data), string(plaintext))
	}
}

func TestHandle_Read_UsesCache(t *testing.T) {
	state, idx, _ := setupUnitState(t)

	plaintext := []byte("cached chunk data")
	chunkID := "cached0001"

	// Put directly in cache, NOT in the mock client
	if err := state.cache.Put(chunkID, plaintext); err != nil {
		t.Fatalf("cache put: %v", err)
	}

	idx.mu.Lock()
	idx.chunks[chunkID] = mockChunkRec{repo: "vol-001", path: "chunks/" + chunkID, refCount: 1}
	idx.files["cached.txt"] = mockFileRec{chunkIDs: []string{chunkID}, size: int64(len(plaintext))}
	idx.mu.Unlock()

	h := &Handle{
		state:       state,
		virtualPath: "cached.txt",
		writable:    false,
	}

	ctx := context.Background()
	dest := make([]byte, 1024)
	result, errno := h.Read(ctx, dest, 0)
	if errno != 0 {
		t.Fatalf("Read returned errno %d", errno)
	}

	buf := make([]byte, 1024)
	data, _ := result.Bytes(buf)
	if string(data) != string(plaintext) {
		t.Fatalf("Read from cache mismatch: got %q, want %q", string(data), string(plaintext))
	}
}

func TestHandle_Read_Offset(t *testing.T) {
	state, idx, client := setupUnitState(t)

	plaintext := []byte("0123456789abcdef")
	chunkID := "offset0001"

	storeEncryptedChunk(t, state, idx, client, chunkID, "vol-001", plaintext)

	idx.mu.Lock()
	idx.files["offset.txt"] = mockFileRec{chunkIDs: []string{chunkID}, size: int64(len(plaintext))}
	idx.mu.Unlock()

	h := &Handle{
		state:       state,
		virtualPath: "offset.txt",
		writable:    false,
	}

	ctx := context.Background()

	// Read from offset 4, 4 bytes
	dest := make([]byte, 4)
	result, errno := h.Read(ctx, dest, 4)
	if errno != 0 {
		t.Fatalf("Read returned errno %d", errno)
	}

	buf := make([]byte, 4)
	data, _ := result.Bytes(buf)
	if string(data) != "4567" {
		t.Fatalf("Read offset mismatch: got %q, want %q", string(data), "4567")
	}
}

func TestHandle_Read_PastEOF(t *testing.T) {
	state, idx, client := setupUnitState(t)

	plaintext := []byte("short")
	chunkID := "eof0001"

	storeEncryptedChunk(t, state, idx, client, chunkID, "vol-001", plaintext)

	idx.mu.Lock()
	idx.files["eof.txt"] = mockFileRec{chunkIDs: []string{chunkID}, size: int64(len(plaintext))}
	idx.mu.Unlock()

	h := &Handle{
		state:       state,
		virtualPath: "eof.txt",
		writable:    false,
	}

	ctx := context.Background()
	dest := make([]byte, 100)
	result, errno := h.Read(ctx, dest, 999)
	if errno != 0 {
		t.Fatalf("Read past EOF returned errno %d", errno)
	}

	buf := make([]byte, 100)
	data, _ := result.Bytes(buf)
	if len(data) != 0 {
		t.Fatalf("Read past EOF should return 0 bytes, got %d", len(data))
	}
}

func TestHandle_Read_MultiChunk(t *testing.T) {
	state, idx, client := setupUnitState(t)

	part1 := []byte("first-chunk-")
	part2 := []byte("second-chunk")
	cid1 := "multi0001"
	cid2 := "multi0002"

	storeEncryptedChunk(t, state, idx, client, cid1, "vol-001", part1)
	storeEncryptedChunk(t, state, idx, client, cid2, "vol-001", part2)

	idx.mu.Lock()
	idx.files["multi.txt"] = mockFileRec{
		chunkIDs: []string{cid1, cid2},
		size:     int64(len(part1) + len(part2)),
	}
	idx.mu.Unlock()

	h := &Handle{
		state:       state,
		virtualPath: "multi.txt",
		writable:    false,
	}

	ctx := context.Background()
	dest := make([]byte, 1024)
	result, errno := h.Read(ctx, dest, 0)
	if errno != 0 {
		t.Fatalf("Read returned errno %d", errno)
	}

	buf := make([]byte, 1024)
	data, _ := result.Bytes(buf)
	want := string(part1) + string(part2)
	if string(data) != want {
		t.Fatalf("Multi-chunk read mismatch: got %q, want %q", string(data), want)
	}
}

func TestHandle_Write_And_Flush(t *testing.T) {
	state, _, _ := setupUnitState(t)

	// Create a temp file simulating what Dir.Create does
	tmpFile, err := os.CreateTemp(state.cache.StagingDir(), "test-write-*")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}

	h := &Handle{
		state:       state,
		virtualPath: "written.txt",
		tmpFile:     tmpFile,
		writable:    true,
	}

	ctx := context.Background()

	// Write some data
	data := []byte("write test data")
	n, errno := h.Write(ctx, data, 0)
	if errno != 0 {
		t.Fatalf("Write returned errno %d", errno)
	}
	if int(n) != len(data) {
		t.Fatalf("Write returned %d bytes, want %d", n, len(data))
	}

	// Verify temp file has the data
	tmpPath := tmpFile.Name()
	tmpFile.Sync()
	content, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatalf("reading temp file: %v", err)
	}
	if !bytes.Equal(content, data) {
		t.Fatalf("temp file content mismatch: got %q, want %q", content, data)
	}

	// Flush without ghfs (would call Put) — we can't test the full upload
	// without wiring FS, but we can verify the temp file is cleaned up by Release
	releaseErrno := h.Release(ctx)
	if releaseErrno != 0 {
		t.Fatalf("Release returned errno %d", releaseErrno)
	}

	// Temp file should be removed
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatal("temp file should be removed after Release")
	}
}

func TestHandle_Write_AtOffset(t *testing.T) {
	state, _, _ := setupUnitState(t)

	tmpFile, err := os.CreateTemp(state.cache.StagingDir(), "test-offset-write-*")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	h := &Handle{
		state:       state,
		virtualPath: "offset-write.txt",
		tmpFile:     tmpFile,
		writable:    true,
	}

	ctx := context.Background()

	// Write "aaaa" at offset 0
	h.Write(ctx, []byte("aaaa"), 0)
	// Write "bbbb" at offset 4
	h.Write(ctx, []byte("bbbb"), 4)

	tmpFile.Sync()
	content, _ := os.ReadFile(tmpFile.Name())
	if string(content) != "aaaabbbb" {
		t.Fatalf("offset write mismatch: got %q, want %q", content, "aaaabbbb")
	}

	h.Release(ctx)
}

func TestHandle_Release_CleansUp(t *testing.T) {
	state, _, _ := setupUnitState(t)

	tmpFile, err := os.CreateTemp(state.cache.StagingDir(), "test-release-*")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Write([]byte("some data"))

	h := &Handle{
		state:       state,
		virtualPath: "release.txt",
		tmpFile:     tmpFile,
		writable:    true,
	}

	ctx := context.Background()
	h.Release(ctx)

	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatal("temp file should be removed after Release")
	}
	if h.tmpFile != nil {
		t.Fatal("tmpFile should be nil after Release")
	}
	if h.data != nil {
		t.Fatal("data should be nil after Release")
	}
}

func TestHandle_Read_NonexistentFile(t *testing.T) {
	state, _, _ := setupUnitState(t)

	h := &Handle{
		state:       state,
		virtualPath: "nonexistent.txt",
		writable:    false,
	}

	ctx := context.Background()
	dest := make([]byte, 100)
	_, errno := h.Read(ctx, dest, 0)
	if errno == 0 {
		t.Fatal("Read of nonexistent file should return error errno")
	}
}

// --- DirTree integration with mountState ---

func TestDirTree_RenameMetadata(t *testing.T) {
	idx := newMockIndex()

	// Simulate a file with chunks
	idx.chunks["chunk1"] = mockChunkRec{repo: "vol-001", path: "chunks/chunk1", refCount: 1}
	idx.files["old.txt"] = mockFileRec{chunkIDs: []string{"chunk1"}, size: 100}

	// Rename: read old, write new, delete old
	chunkIDs, size, err := idx.GetFile("old.txt")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}

	if err := idx.PutFile("new.txt", chunkIDs, size); err != nil {
		t.Fatalf("PutFile: %v", err)
	}

	_, err = idx.DeleteFile("old.txt")
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}

	// Verify old is gone, new exists with same chunks
	_, _, err = idx.GetFile("old.txt")
	if err != store.ErrNotFound {
		t.Fatal("old.txt should be deleted")
	}

	gotIDs, gotSize, err := idx.GetFile("new.txt")
	if err != nil {
		t.Fatalf("GetFile new.txt: %v", err)
	}
	if gotSize != 100 {
		t.Fatalf("size mismatch: got %d, want 100", gotSize)
	}
	if len(gotIDs) != 1 || gotIDs[0] != "chunk1" {
		t.Fatalf("chunkIDs mismatch: got %v, want [chunk1]", gotIDs)
	}

	// Chunk should still exist (ref count from new file)
	_, _, err = idx.GetChunk("chunk1")
	if err != nil {
		t.Fatal("chunk1 should still exist after rename")
	}
}

func TestDirTree_UnlinkUpdatesTree(t *testing.T) {
	tree := NewDirTree()
	tree.AddFile("photos/img.jpg")
	tree.AddFile("docs/readme.txt")

	// Verify files exist
	dirs, files := tree.ReadDir()
	if len(dirs) != 2 {
		t.Fatalf("expected 2 dirs, got %d", len(dirs))
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 root files, got %d", len(files))
	}

	// Remove a file
	tree.RemoveFile("photos/img.jpg")

	// photos dir should be cleaned up (empty)
	dirs, _ = tree.ReadDir()
	if len(dirs) != 1 {
		t.Fatalf("expected 1 dir after remove, got %d: %v", len(dirs), dirs)
	}
	if dirs[0] != "docs" {
		t.Fatalf("remaining dir should be docs, got %s", dirs[0])
	}
}

func TestDirTree_RenameUpdatesTree(t *testing.T) {
	tree := NewDirTree()
	tree.AddFile("old/file.txt")

	// Verify old exists
	sub, ok := tree.Lookup([]string{"old"})
	if !ok {
		t.Fatal("old dir should exist")
	}
	_, files := sub.ReadDir()
	if len(files) != 1 || files[0] != "file.txt" {
		t.Fatalf("expected file.txt in old, got %v", files)
	}

	// Rename
	tree.RemoveFile("old/file.txt")
	tree.AddFile("new/file.txt")

	// Old should be gone (empty dir cleaned up)
	_, ok = tree.Lookup([]string{"old"})
	if ok {
		t.Fatal("old dir should be cleaned up")
	}

	// New should exist
	sub, ok = tree.Lookup([]string{"new"})
	if !ok {
		t.Fatal("new dir should exist")
	}
	_, files = sub.ReadDir()
	if len(files) != 1 || files[0] != "file.txt" {
		t.Fatalf("expected file.txt in new, got %v", files)
	}
}
