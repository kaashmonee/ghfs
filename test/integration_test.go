package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIntegration(t *testing.T) {
	t.Run("FullLifecycle", testFullLifecycle)
	t.Run("Deduplication", testDeduplication)
	t.Run("MultiChunkFile", testMultiChunkFile)
	t.Run("MultipleFiles", testMultipleFiles)
}

func testFullLifecycle(t *testing.T) {
	env, cleanup := setupTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	// Create a temp file with known content.
	content := []byte("Hello, ghfs integration test! This is some test content.")
	localPath := createTempFile(t, content)

	// Put the file.
	if err := env.FS.Put(ctx, localPath, "/test.txt"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Ls should show the file.
	entries, err := env.FS.Ls(ctx, "")
	if err != nil {
		t.Fatalf("Ls: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Path != "/test.txt" {
		t.Errorf("expected path /test.txt, got %s", entries[0].Path)
	}
	if entries[0].Size != int64(len(content)) {
		t.Errorf("expected size %d, got %d", len(content), entries[0].Size)
	}

	// Get the file back.
	outputPath := filepath.Join(t.TempDir(), "output.txt")
	if err := env.FS.Get(ctx, "/test.txt", outputPath); err != nil {
		t.Fatalf("Get: %v", err)
	}
	retrieved, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	if !bytes.Equal(retrieved, content) {
		t.Errorf("content mismatch: got %q, want %q", retrieved, content)
	}

	// Rm the file.
	if err := env.FS.Rm(ctx, "/test.txt"); err != nil {
		t.Fatalf("Rm: %v", err)
	}

	// Ls should be empty.
	entries, err = env.FS.Ls(ctx, "")
	if err != nil {
		t.Fatalf("Ls after Rm: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries after Rm, got %d", len(entries))
	}

	// Chunks should be cleaned up from the mock server.
	chunkKeys := env.MockServer.chunkFiles()
	if len(chunkKeys) != 0 {
		t.Errorf("expected 0 chunk files after Rm, got %d: %v", len(chunkKeys), chunkKeys)
	}
}

func testDeduplication(t *testing.T) {
	env, cleanup := setupTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	// Same content for two files.
	content := []byte("Deduplicated content that should only be stored once in chunks.")
	localPath := createTempFile(t, content)

	if err := env.FS.Put(ctx, localPath, "/file1.txt"); err != nil {
		t.Fatalf("Put file1: %v", err)
	}

	chunksAfterFirst := len(env.MockServer.chunkFiles())
	if chunksAfterFirst == 0 {
		t.Fatal("expected at least one chunk after first Put")
	}

	// Put same content as file2. Chunks should be deduplicated.
	localPath2 := createTempFile(t, content)
	if err := env.FS.Put(ctx, localPath2, "/file2.txt"); err != nil {
		t.Fatalf("Put file2: %v", err)
	}

	chunksAfterSecond := len(env.MockServer.chunkFiles())
	if chunksAfterSecond != chunksAfterFirst {
		t.Errorf("expected %d chunks after dedup, got %d", chunksAfterFirst, chunksAfterSecond)
	}

	// Both files should be listed.
	entries, err := env.FS.Ls(ctx, "")
	if err != nil {
		t.Fatalf("Ls: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Rm file1 — chunks should still exist (referenced by file2).
	if err := env.FS.Rm(ctx, "/file1.txt"); err != nil {
		t.Fatalf("Rm file1: %v", err)
	}
	chunksAfterRm1 := len(env.MockServer.chunkFiles())
	if chunksAfterRm1 != chunksAfterFirst {
		t.Errorf("expected %d chunks after removing file1, got %d (chunks still referenced by file2)",
			chunksAfterFirst, chunksAfterRm1)
	}

	// Rm file2 — now chunks should be cleaned up.
	if err := env.FS.Rm(ctx, "/file2.txt"); err != nil {
		t.Fatalf("Rm file2: %v", err)
	}
	chunksAfterRm2 := len(env.MockServer.chunkFiles())
	if chunksAfterRm2 != 0 {
		t.Errorf("expected 0 chunks after removing both files, got %d", chunksAfterRm2)
	}
}

func testMultiChunkFile(t *testing.T) {
	env, cleanup := setupTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	// Create a file larger than DefaultChunkSize (4MB). Use 5MB of patterned data.
	size := 5 * 1024 * 1024
	content := make([]byte, size)
	// Fill with a repeating pattern that varies enough to not compress trivially.
	for i := range content {
		content[i] = byte(i % 251) // prime modulus for variation
	}

	localPath := createTempFile(t, content)

	if err := env.FS.Put(ctx, localPath, "/bigfile.bin"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Should have 2 chunks (4MB + 1MB).
	chunkKeys := env.MockServer.chunkFiles()
	if len(chunkKeys) != 2 {
		t.Errorf("expected 2 chunks for 5MB file, got %d", len(chunkKeys))
	}

	// Get it back and verify.
	outputPath := filepath.Join(t.TempDir(), "bigfile_out.bin")
	if err := env.FS.Get(ctx, "/bigfile.bin", outputPath); err != nil {
		t.Fatalf("Get: %v", err)
	}
	retrieved, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	if !bytes.Equal(retrieved, content) {
		t.Errorf("content mismatch for multi-chunk file: got %d bytes, want %d bytes", len(retrieved), len(content))
	}
}

func testMultipleFiles(t *testing.T) {
	env, cleanup := setupTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	files := []struct {
		virtualPath string
		content     []byte
	}{
		{"/photos/a.jpg", []byte("fake jpeg content for photo a")},
		{"/photos/b.jpg", []byte("fake jpeg content for photo b, slightly different")},
		{"/docs/c.txt", []byte("this is a text document with some content")},
	}

	for _, f := range files {
		localPath := createTempFile(t, f.content)
		if err := env.FS.Put(ctx, localPath, f.virtualPath); err != nil {
			t.Fatalf("Put %s: %v", f.virtualPath, err)
		}
	}

	// Ls("") should return all 3 files.
	allEntries, err := env.FS.Ls(ctx, "")
	if err != nil {
		t.Fatalf("Ls all: %v", err)
	}
	if len(allEntries) != 3 {
		t.Errorf("expected 3 entries for Ls(\"\"), got %d", len(allEntries))
	}

	// Ls("/photos/") should return 2 files.
	photoEntries, err := env.FS.Ls(ctx, "/photos/")
	if err != nil {
		t.Fatalf("Ls photos: %v", err)
	}
	if len(photoEntries) != 2 {
		t.Errorf("expected 2 entries for Ls(\"/photos/\"), got %d", len(photoEntries))
	}

	// Ls("/docs/") should return 1 file.
	docEntries, err := env.FS.Ls(ctx, "/docs/")
	if err != nil {
		t.Fatalf("Ls docs: %v", err)
	}
	if len(docEntries) != 1 {
		t.Errorf("expected 1 entry for Ls(\"/docs/\"), got %d", len(docEntries))
	}

	// Verify we can retrieve each file correctly.
	for _, f := range files {
		outputPath := filepath.Join(t.TempDir(), filepath.Base(f.virtualPath))
		if err := env.FS.Get(ctx, f.virtualPath, outputPath); err != nil {
			t.Fatalf("Get %s: %v", f.virtualPath, err)
		}
		retrieved, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("reading %s: %v", f.virtualPath, err)
		}
		if !bytes.Equal(retrieved, f.content) {
			t.Errorf("content mismatch for %s", f.virtualPath)
		}
	}
}
