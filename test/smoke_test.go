//go:build smoke

// Smoke tests run against real GitHub. Requires env vars:
//
//	GHFS_TOKEN      - GitHub PAT with repo scope
//	GHFS_OWNER      - GitHub username
//	GHFS_PASSPHRASE - encryption passphrase
//
// Run: go test -tags smoke -v -timeout 120s ./test/...
//
// WARNING: This creates and deletes real GitHub repos (ghfs-smoke-manifest,
// ghfs-smoke-vol-001). Do not run in parallel or against a production account.
package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/berthadev/ghfs/pkg/chunk"
	"github.com/berthadev/ghfs/pkg/fs"
	"github.com/berthadev/ghfs/pkg/github"
	"github.com/berthadev/ghfs/pkg/manifest"
	"github.com/berthadev/ghfs/pkg/store"
	"github.com/berthadev/ghfs/pkg/volume"
)

const (
	smokeManifestRepo = "ghfs-smoke-manifest"
	smokeVolumeRepo   = "ghfs-smoke-vol-001"
)

func smokeEnv(t *testing.T) (token, owner, passphrase string) {
	t.Helper()
	token = os.Getenv("GHFS_TOKEN")
	owner = os.Getenv("GHFS_OWNER")
	passphrase = os.Getenv("GHFS_PASSPHRASE")
	if token == "" || owner == "" || passphrase == "" {
		t.Skip("GHFS_TOKEN, GHFS_OWNER, and GHFS_PASSPHRASE must be set for smoke tests")
	}
	return
}

func TestSmoke(t *testing.T) {
	token, owner, passphrase := smokeEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client := github.NewClient(token, owner)

	// Clean up repos at the end (best effort).
	t.Cleanup(func() {
		deleteRepo(t, token, owner, smokeManifestRepo)
		deleteRepo(t, token, owner, smokeVolumeRepo)
	})

	// --- Init ---
	t.Log("Creating manifest repo...")
	ensureSmokeRepo(t, ctx, client, smokeManifestRepo)

	t.Log("Creating volume repo...")
	ensureSmokeRepo(t, ctx, client, smokeVolumeRepo)

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "smoke.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	// Register the volume.
	_, err = s.DB().Exec(
		`INSERT OR IGNORE INTO volumes (repo, chunk_count, created_at) VALUES (?, 0, datetime('now'))`,
		smokeVolumeRepo,
	)
	if err != nil {
		t.Fatalf("inserting volume: %v", err)
	}

	volMgr := volume.NewManager(client, s.DB(), owner, 1000)
	manifestSvc := manifest.NewService(client, smokeManifestRepo, passphrase)
	fsys := fs.New(s, volMgr, manifestSvc, client, passphrase, chunk.DefaultChunkSize)

	// Push empty manifest.
	t.Log("Pushing empty manifest...")
	m := manifest.NewManifest()
	if err := manifestSvc.Push(ctx, m); err != nil {
		t.Fatalf("manifest.Push: %v", err)
	}

	// Verify manifest round-trip before proceeding.
	t.Log("Verifying manifest pull...")
	pulled, err := manifestSvc.Pull(ctx)
	if err != nil {
		t.Fatalf("manifest.Pull after push: %v", err)
	}
	t.Logf("Manifest pulled OK, files=%d", len(pulled.Files))

	// --- Put ---
	content := []byte("ghfs smoke test content - " + time.Now().Format(time.RFC3339))
	localFile := filepath.Join(tmpDir, "smoke-input.txt")
	if err := os.WriteFile(localFile, content, 0644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	t.Log("Putting file...")
	if err := fsys.Put(ctx, localFile, "smoke-test.txt"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// --- Ls ---
	t.Log("Listing files...")
	entries, err := fsys.Ls(ctx, "")
	if err != nil {
		t.Fatalf("Ls: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Ls: expected 1 entry, got %d", len(entries))
	}
	if entries[0].Path != "smoke-test.txt" {
		t.Fatalf("Ls: expected 'smoke-test.txt', got %q", entries[0].Path)
	}
	t.Logf("Listed: %s (%d bytes)", entries[0].Path, entries[0].Size)

	// --- Get ---
	outputFile := filepath.Join(tmpDir, "smoke-output.txt")
	t.Log("Getting file...")
	if err := fsys.Get(ctx, "smoke-test.txt", outputFile); err != nil {
		t.Fatalf("Get: %v", err)
	}

	got, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch:\n  got:  %q\n  want: %q", got, content)
	}
	t.Log("Get: content matches!")

	// --- Dedup ---
	t.Log("Putting same content under different path (dedup test)...")
	localFile2 := filepath.Join(tmpDir, "smoke-input2.txt")
	if err := os.WriteFile(localFile2, content, 0644); err != nil {
		t.Fatalf("writing test file 2: %v", err)
	}
	if err := fsys.Put(ctx, localFile2, "smoke-test-copy.txt"); err != nil {
		t.Fatalf("Put copy: %v", err)
	}

	entries, err = fsys.Ls(ctx, "")
	if err != nil {
		t.Fatalf("Ls after dedup: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Ls: expected 2 entries after dedup put, got %d", len(entries))
	}
	t.Log("Dedup: second put succeeded without re-uploading chunks")

	// --- Rm ---
	t.Log("Removing files...")
	if err := fsys.Rm(ctx, "smoke-test.txt"); err != nil {
		t.Fatalf("Rm: %v", err)
	}
	if err := fsys.Rm(ctx, "smoke-test-copy.txt"); err != nil {
		t.Fatalf("Rm copy: %v", err)
	}

	entries, err = fsys.Ls(ctx, "")
	if err != nil {
		t.Fatalf("Ls after rm: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("Ls: expected 0 entries after rm, got %d", len(entries))
	}
	t.Log("Rm: all files removed, listing is empty")

	t.Log("Smoke test passed!")
}

func ensureSmokeRepo(t *testing.T, ctx context.Context, client github.ContentAPI, name string) {
	t.Helper()
	exists, err := client.RepoExists(ctx, name)
	if err != nil {
		t.Fatalf("RepoExists(%s): %v", name, err)
	}
	if !exists {
		if err := client.CreateRepo(ctx, name, true); err != nil {
			t.Fatalf("CreateRepo(%s): %v", name, err)
		}
		// Give GitHub a moment to propagate.
		time.Sleep(2 * time.Second)
	}
}

// deleteRepo deletes a GitHub repo via the API (best effort cleanup).
func deleteRepo(t *testing.T, token, owner, name string) {
	t.Helper()
	t.Logf("Cleaning up repo %s/%s...", owner, name)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use a raw HTTP call since our client doesn't have DeleteRepo.
	req, err := newDeleteRepoRequest(ctx, token, owner, name)
	if err != nil {
		t.Logf("warning: could not create delete request for %s: %v", name, err)
		return
	}

	resp, err := clientDo(req)
	if err != nil {
		t.Logf("warning: could not delete repo %s: %v", name, err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode == 204 {
		t.Logf("Deleted repo %s", name)
	} else {
		t.Logf("warning: delete repo %s returned status %d", name, resp.StatusCode)
	}
}
