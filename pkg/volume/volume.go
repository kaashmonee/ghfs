// Package volume manages GitHub repository volumes used as storage backends by ghfs.
package volume

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/berthadev/ghfs/pkg/github"
)

// Allocator defines the interface for volume slot allocation and chunk tracking.
type Allocator interface {
	AllocateSlot(ctx context.Context) (repo string, err error)
	RecordChunk(ctx context.Context, repo string) error
	ReleaseChunk(ctx context.Context, repo string) error
}

// Manager implements Allocator, managing volume repositories and their chunk counts.
type Manager struct {
	client           github.ContentAPI
	db               *sql.DB
	owner            string
	maxChunksPerVol  int
}

// NewManager creates a Manager. If maxChunks is 0 or negative, it defaults to 1000.
func NewManager(client github.ContentAPI, db *sql.DB, owner string, maxChunks int) *Manager {
	if maxChunks <= 0 {
		maxChunks = 1000
	}
	return &Manager{
		client:          client,
		db:              db,
		owner:           owner,
		maxChunksPerVol: maxChunks,
	}
}

// AllocateSlot returns a volume repo with available capacity. If no volume has
// room, it creates a new one with the next sequential name (ghfs-vol-NNN).
func (m *Manager) AllocateSlot(ctx context.Context) (string, error) {
	var repo string
	err := m.db.QueryRowContext(ctx,
		`SELECT repo FROM volumes WHERE chunk_count < ? LIMIT 1`,
		m.maxChunksPerVol,
	).Scan(&repo)
	if err == nil {
		return repo, nil
	}
	if err != sql.ErrNoRows {
		return "", fmt.Errorf("volume: querying available slot: %w", err)
	}

	// No available volume — create a new one.
	name, err := m.nextVolumeName(ctx)
	if err != nil {
		return "", err
	}

	if err := m.client.CreateRepo(ctx, name, true); err != nil {
		return "", fmt.Errorf("volume: creating repo %s: %w", name, err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = m.db.ExecContext(ctx,
		`INSERT INTO volumes (repo, chunk_count, created_at) VALUES (?, 0, ?)`,
		name, now,
	)
	if err != nil {
		return "", fmt.Errorf("volume: inserting volume record: %w", err)
	}

	return name, nil
}

// RecordChunk increments chunk_count for the given volume repo.
func (m *Manager) RecordChunk(ctx context.Context, repo string) error {
	_, err := m.db.ExecContext(ctx,
		`UPDATE volumes SET chunk_count = chunk_count + 1 WHERE repo = ?`,
		repo,
	)
	if err != nil {
		return fmt.Errorf("volume: recording chunk for %s: %w", repo, err)
	}
	return nil
}

// ReleaseChunk decrements chunk_count for the given volume repo.
func (m *Manager) ReleaseChunk(ctx context.Context, repo string) error {
	_, err := m.db.ExecContext(ctx,
		`UPDATE volumes SET chunk_count = chunk_count - 1 WHERE repo = ?`,
		repo,
	)
	if err != nil {
		return fmt.Errorf("volume: releasing chunk for %s: %w", repo, err)
	}
	return nil
}

// nextVolumeName determines the next volume name by finding the highest existing
// volume number and returning the next sequential name in ghfs-vol-NNN format.
func (m *Manager) nextVolumeName(ctx context.Context) (string, error) {
	var maxNum sql.NullInt64
	err := m.db.QueryRowContext(ctx,
		`SELECT MAX(CAST(SUBSTR(repo, 10) AS INTEGER)) FROM volumes WHERE repo LIKE 'ghfs-vol-%'`,
	).Scan(&maxNum)
	if err != nil {
		return "", fmt.Errorf("volume: querying max volume number: %w", err)
	}

	next := 1
	if maxNum.Valid {
		next = int(maxNum.Int64) + 1
	}

	return fmt.Sprintf("ghfs-vol-%03d", next), nil
}

// Compile-time check that Manager implements Allocator.
var _ Allocator = (*Manager)(nil)
