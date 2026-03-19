package manifest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/berthadev/ghfs/pkg/crypto"
)

// mockContentAPI is a test double implementing github.ContentAPI.
type mockContentAPI struct {
	files map[string][]byte // keyed by "repo/path"
}

func newMockContentAPI() *mockContentAPI {
	return &mockContentAPI{files: make(map[string][]byte)}
}

func (m *mockContentAPI) PutFile(_ context.Context, repo, path string, content []byte) (string, error) {
	key := repo + "/" + path
	m.files[key] = content
	return "fake-sha", nil
}

func (m *mockContentAPI) GetFile(_ context.Context, repo, path string) ([]byte, error) {
	key := repo + "/" + path
	data, ok := m.files[key]
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return data, nil
}

func (m *mockContentAPI) DeleteFile(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockContentAPI) CreateRepo(_ context.Context, _ string, _ bool) error {
	return nil
}

func (m *mockContentAPI) RepoExists(_ context.Context, _ string) (bool, error) {
	return true, nil
}

const testPassphrase = "test-passphrase-for-ghfs"

func TestNewManifest(t *testing.T) {
	m := NewManifest()
	if m.Files == nil {
		t.Fatal("NewManifest().Files should not be nil")
	}
	if len(m.Files) != 0 {
		t.Fatalf("NewManifest().Files should be empty, got %d entries", len(m.Files))
	}
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	m := NewManifest()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	m.Files["docs/readme.txt"] = FileRecord{
		ChunkIDs:  []string{"chunk-1", "chunk-2"},
		Size:      2048,
		CreatedAt: now,
	}
	m.Files["photos/cat.jpg"] = FileRecord{
		ChunkIDs:  []string{"chunk-3"},
		Size:      1024000,
		CreatedAt: now,
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Manifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(got.Files))
	}

	rec := got.Files["docs/readme.txt"]
	if len(rec.ChunkIDs) != 2 || rec.ChunkIDs[0] != "chunk-1" || rec.ChunkIDs[1] != "chunk-2" {
		t.Fatalf("unexpected ChunkIDs: %v", rec.ChunkIDs)
	}
	if rec.Size != 2048 {
		t.Fatalf("expected Size 2048, got %d", rec.Size)
	}
	if !rec.CreatedAt.Equal(now) {
		t.Fatalf("expected CreatedAt %v, got %v", now, rec.CreatedAt)
	}
}

func TestPullFileNotFound(t *testing.T) {
	mock := newMockContentAPI()
	svc := NewService(mock, "ghfs-manifest", testPassphrase)

	ctx := context.Background()
	m, err := svc.Pull(ctx)
	if err != nil {
		t.Fatalf("Pull should not error when file missing: %v", err)
	}
	if m == nil || m.Files == nil {
		t.Fatal("Pull should return an initialized manifest")
	}
	if len(m.Files) != 0 {
		t.Fatalf("expected empty manifest, got %d files", len(m.Files))
	}
}

func TestPullExistingManifest(t *testing.T) {
	mock := newMockContentAPI()
	svc := NewService(mock, "ghfs-manifest", testPassphrase)

	// Prepare an encrypted manifest in the mock store.
	original := NewManifest()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	original.Files["hello.txt"] = FileRecord{
		ChunkIDs:  []string{"c1"},
		Size:      42,
		CreatedAt: now,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	encrypted, err := crypto.Encrypt(data, testPassphrase)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	mock.files["ghfs-manifest/manifest.age"] = encrypted

	ctx := context.Background()
	got, err := svc.Pull(ctx)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	rec, ok := got.Files["hello.txt"]
	if !ok {
		t.Fatal("expected hello.txt in pulled manifest")
	}
	if len(rec.ChunkIDs) != 1 || rec.ChunkIDs[0] != "c1" {
		t.Fatalf("unexpected ChunkIDs: %v", rec.ChunkIDs)
	}
	if rec.Size != 42 {
		t.Fatalf("expected Size 42, got %d", rec.Size)
	}
}

func TestPushWritesEncryptedData(t *testing.T) {
	mock := newMockContentAPI()
	svc := NewService(mock, "ghfs-manifest", testPassphrase)

	m := NewManifest()
	m.Files["test.bin"] = FileRecord{
		ChunkIDs:  []string{"a", "b"},
		Size:      100,
		CreatedAt: time.Now().UTC(),
	}

	ctx := context.Background()
	if err := svc.Push(ctx, m); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// Verify data was written to the mock.
	stored, ok := mock.files["ghfs-manifest/manifest.age"]
	if !ok {
		t.Fatal("expected manifest.age to be written")
	}

	// Verify the stored data is encrypted (can be decrypted).
	plaintext, err := crypto.Decrypt(stored, testPassphrase)
	if err != nil {
		t.Fatalf("stored data should be decryptable: %v", err)
	}

	var got Manifest
	if err := json.Unmarshal(plaintext, &got); err != nil {
		t.Fatalf("stored data should unmarshal: %v", err)
	}

	if len(got.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(got.Files))
	}
	rec := got.Files["test.bin"]
	if rec.Size != 100 {
		t.Fatalf("expected Size 100, got %d", rec.Size)
	}
}

func TestPushPullRoundTrip(t *testing.T) {
	mock := newMockContentAPI()
	svc := NewService(mock, "ghfs-manifest", testPassphrase)
	ctx := context.Background()

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	original := NewManifest()
	original.Files["a/b/c.txt"] = FileRecord{
		ChunkIDs:  []string{"x1", "x2", "x3"},
		Size:      999,
		CreatedAt: now,
	}
	original.Files["d.dat"] = FileRecord{
		ChunkIDs:  []string{"y1"},
		Size:      1,
		CreatedAt: now,
	}

	if err := svc.Push(ctx, original); err != nil {
		t.Fatalf("Push: %v", err)
	}

	got, err := svc.Pull(ctx)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if len(got.Files) != len(original.Files) {
		t.Fatalf("expected %d files, got %d", len(original.Files), len(got.Files))
	}

	for path, origRec := range original.Files {
		gotRec, ok := got.Files[path]
		if !ok {
			t.Fatalf("missing file %s after round-trip", path)
		}
		if gotRec.Size != origRec.Size {
			t.Fatalf("%s: expected Size %d, got %d", path, origRec.Size, gotRec.Size)
		}
		if !gotRec.CreatedAt.Equal(origRec.CreatedAt) {
			t.Fatalf("%s: expected CreatedAt %v, got %v", path, origRec.CreatedAt, gotRec.CreatedAt)
		}
		if len(gotRec.ChunkIDs) != len(origRec.ChunkIDs) {
			t.Fatalf("%s: expected %d chunks, got %d", path, len(origRec.ChunkIDs), len(gotRec.ChunkIDs))
		}
		for i, id := range origRec.ChunkIDs {
			if gotRec.ChunkIDs[i] != id {
				t.Fatalf("%s: chunk %d: expected %s, got %s", path, i, id, gotRec.ChunkIDs[i])
			}
		}
	}
}
