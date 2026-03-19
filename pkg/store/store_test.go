package store

import (
	"os"
	"path/filepath"
	"testing"
)

func tempDB(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(%q): %v", dbPath, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sub", "nested", "index.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database file not created: %v", err)
	}
}

func TestPutGetChunkRoundTrip(t *testing.T) {
	s := tempDB(t)

	if err := s.PutChunk("abc123", "myrepo", "objects/ab/c123"); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	repo, path, err := s.GetChunk("abc123")
	if err != nil {
		t.Fatalf("GetChunk: %v", err)
	}
	if repo != "myrepo" {
		t.Errorf("repo = %q, want %q", repo, "myrepo")
	}
	if path != "objects/ab/c123" {
		t.Errorf("path = %q, want %q", path, "objects/ab/c123")
	}
}

func TestGetChunkNotFound(t *testing.T) {
	s := tempDB(t)

	_, _, err := s.GetChunk("nonexistent")
	if err != ErrNotFound {
		t.Fatalf("GetChunk(nonexistent) err = %v, want ErrNotFound", err)
	}
}

func TestChunkExists(t *testing.T) {
	s := tempDB(t)

	exists, err := s.ChunkExists("abc123")
	if err != nil {
		t.Fatalf("ChunkExists: %v", err)
	}
	if exists {
		t.Error("ChunkExists returned true for non-existent chunk")
	}

	if err := s.PutChunk("abc123", "repo", "path"); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	exists, err = s.ChunkExists("abc123")
	if err != nil {
		t.Fatalf("ChunkExists: %v", err)
	}
	if !exists {
		t.Error("ChunkExists returned false for existing chunk")
	}
}

func TestPutGetFileRoundTrip(t *testing.T) {
	s := tempDB(t)

	// Put chunks first.
	for _, cid := range []string{"c1", "c2", "c3"} {
		if err := s.PutChunk(cid, "repo", "path/"+cid); err != nil {
			t.Fatalf("PutChunk(%s): %v", cid, err)
		}
	}

	chunkIDs := []string{"c1", "c2", "c3"}
	if err := s.PutFile("/photos/cat.jpg", chunkIDs, 12345); err != nil {
		t.Fatalf("PutFile: %v", err)
	}

	gotIDs, gotSize, err := s.GetFile("/photos/cat.jpg")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if gotSize != 12345 {
		t.Errorf("size = %d, want 12345", gotSize)
	}
	if len(gotIDs) != 3 {
		t.Fatalf("len(chunkIDs) = %d, want 3", len(gotIDs))
	}
	for i, want := range chunkIDs {
		if gotIDs[i] != want {
			t.Errorf("chunkIDs[%d] = %q, want %q", i, gotIDs[i], want)
		}
	}
}

func TestGetFileNotFound(t *testing.T) {
	s := tempDB(t)

	_, _, err := s.GetFile("/no/such/file")
	if err != ErrNotFound {
		t.Fatalf("GetFile(nonexistent) err = %v, want ErrNotFound", err)
	}
}

func TestListFilesPrefix(t *testing.T) {
	s := tempDB(t)

	if err := s.PutChunk("c1", "r", "p"); err != nil {
		t.Fatal(err)
	}

	if err := s.PutFile("/photos/a.jpg", []string{"c1"}, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFile("/photos/b.png", []string{"c1"}, 200); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFile("/docs/b.txt", []string{"c1"}, 300); err != nil {
		t.Fatal(err)
	}

	entries, err := s.ListFiles("/photos/")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}

	paths := map[string]bool{}
	for _, e := range entries {
		paths[e.Path] = true
	}
	if !paths["/photos/a.jpg"] {
		t.Error("missing /photos/a.jpg")
	}
	if !paths["/photos/b.png"] {
		t.Error("missing /photos/b.png")
	}
	if paths["/docs/b.txt"] {
		t.Error("should not include /docs/b.txt")
	}
}

func TestRefCounting(t *testing.T) {
	s := tempDB(t)

	// Insert a shared chunk.
	if err := s.PutChunk("shared", "repo", "path/shared"); err != nil {
		t.Fatal(err)
	}

	// File A references the shared chunk.
	if err := s.PutFile("/a.txt", []string{"shared"}, 10); err != nil {
		t.Fatal(err)
	}

	// File B also references the shared chunk.
	if err := s.PutFile("/b.txt", []string{"shared"}, 20); err != nil {
		t.Fatal(err)
	}

	// Delete file A: chunk should NOT be orphaned (ref_count goes from 2 to 1).
	orphaned, err := s.DeleteFile("/a.txt")
	if err != nil {
		t.Fatalf("DeleteFile(/a.txt): %v", err)
	}
	if len(orphaned) != 0 {
		t.Errorf("expected no orphaned chunks after deleting /a.txt, got %v", orphaned)
	}

	// Delete file B: chunk should be orphaned (ref_count goes from 1 to 0).
	orphaned, err = s.DeleteFile("/b.txt")
	if err != nil {
		t.Fatalf("DeleteFile(/b.txt): %v", err)
	}
	if len(orphaned) != 1 || orphaned[0] != "shared" {
		t.Errorf("expected orphaned=[shared], got %v", orphaned)
	}
}

func TestDeleteChunk(t *testing.T) {
	s := tempDB(t)

	if err := s.PutChunk("todelete", "repo", "path"); err != nil {
		t.Fatal(err)
	}

	exists, _ := s.ChunkExists("todelete")
	if !exists {
		t.Fatal("chunk should exist before delete")
	}

	if err := s.DeleteChunk("todelete"); err != nil {
		t.Fatalf("DeleteChunk: %v", err)
	}

	exists, _ = s.ChunkExists("todelete")
	if exists {
		t.Error("chunk should not exist after delete")
	}
}
