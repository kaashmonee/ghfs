package volume

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/berthadev/ghfs/pkg/store"

	_ "modernc.org/sqlite"
)

// mockContentAPI is a test double for github.ContentAPI.
type mockContentAPI struct {
	createdRepos []string
}

func (m *mockContentAPI) PutFile(_ context.Context, _, _ string, _ []byte) (string, error) {
	return "sha256", nil
}

func (m *mockContentAPI) GetFile(_ context.Context, _, _ string) ([]byte, error) {
	return nil, nil
}

func (m *mockContentAPI) DeleteFile(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockContentAPI) CreateRepo(_ context.Context, name string, _ bool) error {
	m.createdRepos = append(m.createdRepos, name)
	return nil
}

func (m *mockContentAPI) RepoExists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

// newTestManager creates a Manager backed by a temporary SQLite database.
func newTestManager(t *testing.T, maxChunks int) (*Manager, *mockContentAPI) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Open a second connection to the same SQLite file for the Manager.
	// The schema was already created by store.Open above.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mock := &mockContentAPI{}
	mgr := NewManager(mock, db, "testowner", maxChunks)
	return mgr, mock
}

func TestAllocateSlotCreatesFirstVolume(t *testing.T) {
	mgr, mock := newTestManager(t, 10)
	ctx := context.Background()

	repo, err := mgr.AllocateSlot(ctx)
	if err != nil {
		t.Fatalf("AllocateSlot: %v", err)
	}
	if repo != "ghfs-vol-001" {
		t.Errorf("got repo %q, want ghfs-vol-001", repo)
	}
	if len(mock.createdRepos) != 1 || mock.createdRepos[0] != "ghfs-vol-001" {
		t.Errorf("expected CreateRepo called with ghfs-vol-001, got %v", mock.createdRepos)
	}
}

func TestAllocateSlotReusesExistingVolume(t *testing.T) {
	mgr, mock := newTestManager(t, 10)
	ctx := context.Background()

	// Create first volume.
	repo1, err := mgr.AllocateSlot(ctx)
	if err != nil {
		t.Fatalf("AllocateSlot: %v", err)
	}

	// Record a few chunks but stay under capacity.
	for range 3 {
		if err := mgr.RecordChunk(ctx, repo1); err != nil {
			t.Fatalf("RecordChunk: %v", err)
		}
	}

	// Next allocation should reuse the same volume.
	repo2, err := mgr.AllocateSlot(ctx)
	if err != nil {
		t.Fatalf("AllocateSlot: %v", err)
	}
	if repo2 != repo1 {
		t.Errorf("expected reuse of %q, got %q", repo1, repo2)
	}
	// CreateRepo should only have been called once.
	if len(mock.createdRepos) != 1 {
		t.Errorf("expected 1 CreateRepo call, got %d", len(mock.createdRepos))
	}
}

func TestAllocateSlotCreatesNewWhenFull(t *testing.T) {
	mgr, mock := newTestManager(t, 2)
	ctx := context.Background()

	repo1, err := mgr.AllocateSlot(ctx)
	if err != nil {
		t.Fatalf("AllocateSlot: %v", err)
	}

	// Fill volume to capacity.
	for range 2 {
		if err := mgr.RecordChunk(ctx, repo1); err != nil {
			t.Fatalf("RecordChunk: %v", err)
		}
	}

	// Next allocation should create a new volume.
	repo2, err := mgr.AllocateSlot(ctx)
	if err != nil {
		t.Fatalf("AllocateSlot: %v", err)
	}
	if repo2 != "ghfs-vol-002" {
		t.Errorf("got repo %q, want ghfs-vol-002", repo2)
	}
	if len(mock.createdRepos) != 2 {
		t.Errorf("expected 2 CreateRepo calls, got %d", len(mock.createdRepos))
	}
}

func TestRecordChunkIncrements(t *testing.T) {
	mgr, _ := newTestManager(t, 10)
	ctx := context.Background()

	repo, err := mgr.AllocateSlot(ctx)
	if err != nil {
		t.Fatalf("AllocateSlot: %v", err)
	}

	for range 5 {
		if err := mgr.RecordChunk(ctx, repo); err != nil {
			t.Fatalf("RecordChunk: %v", err)
		}
	}

	// Verify chunk count.
	var count int
	err = mgr.db.QueryRow(`SELECT chunk_count FROM volumes WHERE repo = ?`, repo).Scan(&count)
	if err != nil {
		t.Fatalf("querying chunk_count: %v", err)
	}
	if count != 5 {
		t.Errorf("chunk_count = %d, want 5", count)
	}
}

func TestReleaseChunkDecrements(t *testing.T) {
	mgr, _ := newTestManager(t, 10)
	ctx := context.Background()

	repo, err := mgr.AllocateSlot(ctx)
	if err != nil {
		t.Fatalf("AllocateSlot: %v", err)
	}

	// Record 3 chunks, release 1.
	for range 3 {
		if err := mgr.RecordChunk(ctx, repo); err != nil {
			t.Fatalf("RecordChunk: %v", err)
		}
	}
	if err := mgr.ReleaseChunk(ctx, repo); err != nil {
		t.Fatalf("ReleaseChunk: %v", err)
	}

	var count int
	err = mgr.db.QueryRow(`SELECT chunk_count FROM volumes WHERE repo = ?`, repo).Scan(&count)
	if err != nil {
		t.Fatalf("querying chunk_count: %v", err)
	}
	if count != 2 {
		t.Errorf("chunk_count = %d, want 2", count)
	}
}

func TestSequentialNaming(t *testing.T) {
	mgr, mock := newTestManager(t, 1)
	ctx := context.Background()

	expected := []string{"ghfs-vol-001", "ghfs-vol-002", "ghfs-vol-003"}
	for i, want := range expected {
		repo, err := mgr.AllocateSlot(ctx)
		if err != nil {
			t.Fatalf("AllocateSlot[%d]: %v", i, err)
		}
		if repo != want {
			t.Errorf("AllocateSlot[%d] = %q, want %q", i, repo, want)
		}
		// Fill the volume so the next allocation creates a new one.
		if err := mgr.RecordChunk(ctx, repo); err != nil {
			t.Fatalf("RecordChunk[%d]: %v", i, err)
		}
	}
	if len(mock.createdRepos) != 3 {
		t.Errorf("expected 3 CreateRepo calls, got %d", len(mock.createdRepos))
	}
}
