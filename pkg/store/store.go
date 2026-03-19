// Package store provides a SQLite-backed local index for ghfs metadata.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// FileEntry represents a file record returned by ListFiles.
type FileEntry struct {
	Path      string
	Size      int64
	CreatedAt string
}

// Index defines the public interface for the local metadata index.
type Index interface {
	PutChunk(chunkID, repo, path string) error
	GetChunk(chunkID string) (repo string, path string, err error)
	ChunkExists(chunkID string) (bool, error)
	PutFile(virtualPath string, chunkIDs []string, size int64) error
	GetFile(virtualPath string) (chunkIDs []string, size int64, err error)
	ListFiles(prefix string) ([]FileEntry, error)
	DeleteFile(virtualPath string) (orphanedChunkIDs []string, err error)
	DeleteChunk(chunkID string) error
	Close() error
}

// Store is a SQLite-backed implementation of Index.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite database at dbPath, creates parent
// directories if needed, and ensures all required tables exist.
func Open(dbPath string) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	schema := `
CREATE TABLE IF NOT EXISTS chunks (
	chunk_id  TEXT PRIMARY KEY,
	repo      TEXT NOT NULL,
	path      TEXT NOT NULL,
	ref_count INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS files (
	virtual_path TEXT PRIMARY KEY,
	chunk_ids    TEXT NOT NULL,
	size         INTEGER,
	created_at   TEXT
);
CREATE TABLE IF NOT EXISTS volumes (
	repo        TEXT PRIMARY KEY,
	chunk_count INTEGER DEFAULT 0,
	created_at  TEXT
);`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

// PutChunk inserts a chunk record. If the chunk already exists, it is ignored
// (INSERT OR IGNORE). The ref_count starts at 0 and is incremented by PutFile.
func (s *Store) PutChunk(chunkID, repo, path string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO chunks (chunk_id, repo, path, ref_count) VALUES (?, ?, ?, 0)`,
		chunkID, repo, path,
	)
	return err
}

// GetChunk retrieves the repo and path for a given chunk ID.
// Returns ErrNotFound if the chunk does not exist.
func (s *Store) GetChunk(chunkID string) (string, string, error) {
	var repo, path string
	err := s.db.QueryRow(
		`SELECT repo, path FROM chunks WHERE chunk_id = ?`, chunkID,
	).Scan(&repo, &path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	return repo, path, nil
}

// ChunkExists reports whether a chunk with the given ID exists.
func (s *Store) ChunkExists(chunkID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM chunks WHERE chunk_id = ?`, chunkID,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// PutFile inserts a file record with JSON-encoded chunk IDs and the current
// timestamp, then increments ref_count for each referenced chunk.
func (s *Store) PutFile(virtualPath string, chunkIDs []string, size int64) error {
	data, err := json.Marshal(chunkIDs)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		`INSERT OR REPLACE INTO files (virtual_path, chunk_ids, size, created_at) VALUES (?, ?, ?, ?)`,
		virtualPath, string(data), size, now,
	)
	if err != nil {
		return err
	}

	for _, cid := range chunkIDs {
		_, err = tx.Exec(
			`UPDATE chunks SET ref_count = ref_count + 1 WHERE chunk_id = ?`, cid,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetFile retrieves the chunk IDs and size for the given virtual path.
// Returns ErrNotFound if the file does not exist.
func (s *Store) GetFile(virtualPath string) ([]string, int64, error) {
	var raw string
	var size int64
	err := s.db.QueryRow(
		`SELECT chunk_ids, size FROM files WHERE virtual_path = ?`, virtualPath,
	).Scan(&raw, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}

	var chunkIDs []string
	if err := json.Unmarshal([]byte(raw), &chunkIDs); err != nil {
		return nil, 0, err
	}
	return chunkIDs, size, nil
}

// ListFiles returns all file entries whose virtual_path starts with prefix.
func (s *Store) ListFiles(prefix string) ([]FileEntry, error) {
	rows, err := s.db.Query(
		`SELECT virtual_path, size, created_at FROM files WHERE virtual_path LIKE ?`,
		prefix+"%",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []FileEntry
	for rows.Next() {
		var e FileEntry
		if err := rows.Scan(&e.Path, &e.Size, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// DeleteFile removes a file record and decrements ref_count for each of its
// chunks. It returns the IDs of chunks whose ref_count dropped to 0 or below
// (orphaned chunks).
func (s *Store) DeleteFile(virtualPath string) ([]string, error) {
	// First, get the file's chunk IDs.
	var raw string
	err := s.db.QueryRow(
		`SELECT chunk_ids FROM files WHERE virtual_path = ?`, virtualPath,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	var chunkIDs []string
	if err := json.Unmarshal([]byte(raw), &chunkIDs); err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Delete the file row.
	_, err = tx.Exec(`DELETE FROM files WHERE virtual_path = ?`, virtualPath)
	if err != nil {
		return nil, err
	}

	// Decrement ref_count for each chunk.
	for _, cid := range chunkIDs {
		_, err = tx.Exec(
			`UPDATE chunks SET ref_count = ref_count - 1 WHERE chunk_id = ?`, cid,
		)
		if err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// Collect orphaned chunks (ref_count <= 0).
	var orphaned []string
	for _, cid := range chunkIDs {
		var refCount int
		err := s.db.QueryRow(
			`SELECT ref_count FROM chunks WHERE chunk_id = ?`, cid,
		).Scan(&refCount)
		if err != nil {
			return nil, err
		}
		if refCount <= 0 {
			orphaned = append(orphaned, cid)
		}
	}

	return orphaned, nil
}

// DeleteChunk removes a chunk record from the index.
func (s *Store) DeleteChunk(chunkID string) error {
	_, err := s.db.Exec(`DELETE FROM chunks WHERE chunk_id = ?`, chunkID)
	return err
}

// DB returns the underlying *sql.DB so callers (e.g., volume.Manager) can share
// the same database connection.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}
