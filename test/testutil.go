// Package integration provides end-to-end integration tests for ghfs.
package integration

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/berthadev/ghfs/pkg/chunk"
	"github.com/berthadev/ghfs/pkg/fs"
	"github.com/berthadev/ghfs/pkg/github"
	"github.com/berthadev/ghfs/pkg/manifest"
	"github.com/berthadev/ghfs/pkg/store"
	"github.com/berthadev/ghfs/pkg/volume"
)

const testPassphrase = "test-passphrase-123"
const testOwner = "testowner"

// mockGitHubServer holds the state for a mock GitHub Contents API server.
type mockGitHubServer struct {
	mu    sync.Mutex
	files map[string]fileRecord // key: "repo/path"
	repos map[string]bool       // set of created repo names
}

type fileRecord struct {
	content []byte // raw bytes stored
	sha     string
}

// contentsGetResponse is the JSON shape returned by GET /repos/:owner/:repo/contents/:path.
type contentsGetResponse struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	SHA      string `json:"sha"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// putRequest is the JSON shape of a PUT /repos/:owner/:repo/contents/:path body.
type putRequest struct {
	Message string `json:"message"`
	Content string `json:"content"`
	SHA     string `json:"sha,omitempty"`
}

// putResponse is the JSON shape returned by PUT /repos/:owner/:repo/contents/:path.
type putResponse struct {
	Content struct {
		SHA string `json:"sha"`
	} `json:"content"`
}

// deleteRequest is the JSON shape of a DELETE body.
type deleteRequest struct {
	Message string `json:"message"`
	SHA     string `json:"sha"`
}

// createRepoRequest is the JSON shape of POST /user/repos body.
type createRepoRequest struct {
	Name    string `json:"name"`
	Private bool   `json:"private"`
}

func computeSHA(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func newMockGitHubServer() *mockGitHubServer {
	return &mockGitHubServer{
		files: make(map[string]fileRecord),
		repos: make(map[string]bool),
	}
}

func (m *mockGitHubServer) handler() http.Handler {
	mux := http.NewServeMux()

	// POST /user/repos — create repo
	mux.HandleFunc("POST /user/repos", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var req createRepoRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		m.repos[req.Name] = true
		m.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"name":"%s"}`, req.Name)
	})

	// Contents API: /repos/{owner}/{repo}/contents/{path...}
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		// Parse path: /repos/{owner}/{repo}/contents/{path...}
		// or /repos/{owner}/{repo} (repo exists check)
		trimmed := strings.TrimPrefix(r.URL.Path, "/repos/")
		parts := strings.SplitN(trimmed, "/", 3) // owner, repo, rest

		if len(parts) < 2 {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}

		repo := parts[1]

		// GET /repos/{owner}/{repo} — repo exists check (no "contents" part)
		if len(parts) == 2 || parts[2] == "" {
			if r.Method == http.MethodGet {
				m.mu.Lock()
				exists := m.repos[repo]
				m.mu.Unlock()
				if exists {
					w.WriteHeader(http.StatusOK)
					fmt.Fprintf(w, `{"name":"%s"}`, repo)
				} else {
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, `{"message":"Not Found"}`)
				}
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Must start with "contents/"
		rest := parts[2]
		if !strings.HasPrefix(rest, "contents/") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		filePath := strings.TrimPrefix(rest, "contents/")
		key := repo + "/" + filePath

		switch r.Method {
		case http.MethodGet:
			m.mu.Lock()
			rec, ok := m.files[key]
			m.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"message":"Not Found"}`)
				return
			}
			resp := contentsGetResponse{
				Name:     filepath.Base(filePath),
				Path:     filePath,
				SHA:      rec.sha,
				Content:  base64.StdEncoding.EncodeToString(rec.content),
				Encoding: "base64",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			var req putRequest
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			decoded, err := base64.StdEncoding.DecodeString(req.Content)
			if err != nil {
				http.Error(w, "bad base64", http.StatusBadRequest)
				return
			}
			sha := computeSHA(decoded)
			m.mu.Lock()
			m.files[key] = fileRecord{content: decoded, sha: sha}
			m.mu.Unlock()

			resp := putResponse{}
			resp.Content.SHA = sha
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)

		case http.MethodDelete:
			m.mu.Lock()
			delete(m.files, key)
			m.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{}`)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	return mux
}

// fileCount returns the number of files stored in the mock server.
func (m *mockGitHubServer) fileCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.files)
}

// chunkFiles returns the keys that contain "chunks/" in their path.
func (m *mockGitHubServer) chunkFiles() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []string
	for k := range m.files {
		if strings.Contains(k, "chunks/") {
			result = append(result, k)
		}
	}
	return result
}

// testEnv holds all the wired-up components for an integration test.
type testEnv struct {
	FS         *fs.FS
	Store      *store.Store
	MockServer *mockGitHubServer
	TmpDir     string
}

// setupTestEnv creates the full test environment with real packages and a mock
// GitHub HTTP server. Returns a testEnv and a cleanup function.
func setupTestEnv(t *testing.T) (*testEnv, func()) {
	t.Helper()

	mock := newMockGitHubServer()
	server := httptest.NewServer(mock.handler())

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "ghfs.db")

	client := github.NewClientWithBaseURL("fake-token", testOwner, server.URL)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	volMgr := volume.NewManager(client, s.DB(), testOwner, 1000)

	manifestRepo := "ghfs-manifest"
	// Pre-register the manifest repo so RepoExists works.
	mock.mu.Lock()
	mock.repos[manifestRepo] = true
	mock.mu.Unlock()

	manifestSvc := manifest.NewService(client, manifestRepo, testPassphrase)

	fsys := fs.New(s, volMgr, manifestSvc, client, testPassphrase, chunk.DefaultChunkSize)

	cleanup := func() {
		s.Close()
		server.Close()
	}

	return &testEnv{
		FS:         fsys,
		Store:      s,
		MockServer: mock,
		TmpDir:     tmpDir,
	}, cleanup
}

// createTempFile creates a temporary file with the given content and returns its path.
func createTempFile(t *testing.T, content []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "ghfs-test-*")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing temp file: %v", err)
	}
	return f.Name()
}
