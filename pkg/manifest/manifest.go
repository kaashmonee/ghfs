// Package manifest manages the ghfs file manifest, which tracks virtual file
// paths and their associated chunk IDs. The manifest is stored encrypted in a
// GitHub repository using the age encryption scheme.
package manifest

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/berthadev/ghfs/pkg/crypto"
	"github.com/berthadev/ghfs/pkg/github"
)

// FileRecord describes a single virtual file in the manifest.
type FileRecord struct {
	ChunkIDs  []string  `json:"chunk_ids"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// Manifest holds the mapping of virtual paths to file records.
type Manifest struct {
	Files map[string]FileRecord `json:"files"`
}

// NewManifest returns a Manifest with an initialized empty Files map.
func NewManifest() *Manifest {
	return &Manifest{
		Files: make(map[string]FileRecord),
	}
}

// ManifestSync defines operations for pulling and pushing the manifest
// to a remote store.
type ManifestSync interface {
	Pull(ctx context.Context) (*Manifest, error)
	Push(ctx context.Context, m *Manifest) error
}

// Service implements ManifestSync by storing an encrypted manifest in a GitHub
// repository via the ContentAPI.
type Service struct {
	client     github.ContentAPI
	repo       string
	passphrase string
}

// NewService creates a Service that reads and writes the manifest to the given
// repo using the provided ContentAPI client and encryption passphrase.
func NewService(client github.ContentAPI, repo, passphrase string) *Service {
	return &Service{
		client:     client,
		repo:       repo,
		passphrase: passphrase,
	}
}

// manifestFile is the path within the repo where the encrypted manifest is stored.
const manifestFile = "manifest.age"

// Pull retrieves and decrypts the manifest from the GitHub repository.
// If the manifest file does not exist, an empty manifest is returned.
func (s *Service) Pull(ctx context.Context) (*Manifest, error) {
	data, err := s.client.GetFile(ctx, s.repo, manifestFile)
	if err != nil {
		// File does not exist yet — return an empty manifest.
		return NewManifest(), nil
	}

	plaintext, err := crypto.Decrypt(data, s.passphrase)
	if err != nil {
		return nil, fmt.Errorf("manifest: decrypting: %w", err)
	}

	var m Manifest
	if err := json.Unmarshal(plaintext, &m); err != nil {
		return nil, fmt.Errorf("manifest: unmarshaling: %w", err)
	}

	return &m, nil
}

// Push encrypts and writes the manifest to the GitHub repository.
// PutFile handles create-vs-update internally, so no SHA tracking is needed.
func (s *Service) Push(ctx context.Context, m *Manifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("manifest: marshaling: %w", err)
	}

	encrypted, err := crypto.Encrypt(data, s.passphrase)
	if err != nil {
		return fmt.Errorf("manifest: encrypting: %w", err)
	}

	_, err = s.client.PutFile(ctx, s.repo, manifestFile, encrypted)
	if err != nil {
		return fmt.Errorf("manifest: pushing: %w", err)
	}

	return nil
}

// Compile-time check that Service implements ManifestSync.
var _ ManifestSync = (*Service)(nil)
