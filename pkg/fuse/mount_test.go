//go:build fuse

package ghfuse

import (
	"context"
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
	"time"

	gofuse "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/berthadev/ghfs/pkg/cache"
	"github.com/berthadev/ghfs/pkg/chunk"
	"github.com/berthadev/ghfs/pkg/fs"
	"github.com/berthadev/ghfs/pkg/github"
	"github.com/berthadev/ghfs/pkg/manifest"
	"github.com/berthadev/ghfs/pkg/store"
	"github.com/berthadev/ghfs/pkg/volume"
)

const testPassphrase = "test-passphrase-123"
const testOwner = "testowner"

// mockGitHub holds the state for a mock GitHub Contents API server.
type mockGitHub struct {
	mu    sync.Mutex
	files map[string]mockFileRecord // key: "repo/path"
	repos map[string]bool
}

type mockFileRecord struct {
	content []byte
	sha     string
}

func mockComputeSHA(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func newMockGitHub() *mockGitHub {
	return &mockGitHub{
		files: make(map[string]mockFileRecord),
		repos: make(map[string]bool),
	}
}

type mockPutRequest struct {
	Message string `json:"message"`
	Content string `json:"content"`
	SHA     string `json:"sha,omitempty"`
}

type mockPutResponse struct {
	Content struct {
		SHA string `json:"sha"`
	} `json:"content"`
}

type mockContentsGetResponse struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	SHA      string `json:"sha"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

type mockCreateRepoRequest struct {
	Name    string `json:"name"`
	Private bool   `json:"private"`
}

func (m *mockGitHub) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /user/repos", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var req mockCreateRepoRequest
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

	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		trimmed := strings.TrimPrefix(r.URL.Path, "/repos/")
		parts := strings.SplitN(trimmed, "/", 3)

		if len(parts) < 2 {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}

		repo := parts[1]

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
			resp := mockContentsGetResponse{
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
			var req mockPutRequest
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			decoded, err := base64.StdEncoding.DecodeString(req.Content)
			if err != nil {
				http.Error(w, "bad base64", http.StatusBadRequest)
				return
			}
			sha := mockComputeSHA(decoded)
			m.mu.Lock()
			m.files[key] = mockFileRecord{content: decoded, sha: sha}
			m.mu.Unlock()

			resp := mockPutResponse{}
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

func ptrDuration(d time.Duration) *time.Duration {
	return &d
}

// fuseTestEnv holds all the wired-up components for a FUSE integration test.
type fuseTestEnv struct {
	fsOrch     *fs.FS
	idx        *store.Store
	cch        *cache.Cache
	client     github.ContentAPI
	mock       *mockGitHub
	tree       *DirTree
	mountpoint string
	tmpDir     string
}

// setupFuseEnv creates the full test environment with real packages and a mock
// GitHub HTTP server, suitable for mounting FUSE.
func setupFuseEnv(t *testing.T) *fuseTestEnv {
	t.Helper()

	mock := newMockGitHub()
	server := httptest.NewServer(mock.handler())
	t.Cleanup(server.Close)

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "ghfs.db")

	client := github.NewClientWithBaseURL("fake-token", testOwner, server.URL)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	volMgr := volume.NewManager(client, s.DB(), testOwner, 1000)

	manifestRepo := "ghfs-manifest"
	mock.mu.Lock()
	mock.repos[manifestRepo] = true
	mock.mu.Unlock()
	manifestSvc := manifest.NewService(client, manifestRepo, testPassphrase)

	fsOrch := fs.New(s, volMgr, manifestSvc, client, testPassphrase, chunk.DefaultChunkSize)

	cacheDir := filepath.Join(tmpDir, "cache")
	cch, err := cache.New(cacheDir, 100*1024*1024) // 100 MB
	if err != nil {
		t.Fatalf("creating cache: %v", err)
	}

	mountpoint := filepath.Join(tmpDir, "mnt")
	if err := os.MkdirAll(mountpoint, 0755); err != nil {
		t.Fatalf("creating mountpoint: %v", err)
	}

	tree := NewDirTree()

	return &fuseTestEnv{
		fsOrch:     fsOrch,
		idx:        s,
		cch:        cch,
		client:     client,
		mock:       mock,
		tree:       tree,
		mountpoint: mountpoint,
		tmpDir:     tmpDir,
	}
}

// prepopulateFile creates a local temp file with the given content, uses fs.Put
// to store it through the full pipeline, and adds it to the DirTree.
func (e *fuseTestEnv) prepopulateFile(t *testing.T, virtualPath string, content []byte) {
	t.Helper()
	ctx := context.Background()

	tmpFile := filepath.Join(e.tmpDir, "prepop-"+filepath.Base(virtualPath))
	if err := os.WriteFile(tmpFile, content, 0644); err != nil {
		t.Fatalf("writing prepopulate temp file: %v", err)
	}

	if err := e.fsOrch.Put(ctx, tmpFile, virtualPath); err != nil {
		t.Fatalf("prepopulating file %q: %v", virtualPath, err)
	}

	e.tree.AddFile(virtualPath)
}

// mountFUSE mounts the FUSE filesystem directly using go-fuse, bypassing the
// Mount wrapper to allow proper server control and zero-cache configuration.
// Returns the FUSE server for unmounting.
func (e *fuseTestEnv) mountFUSE(t *testing.T) *fuse.Server {
	t.Helper()

	state := NewMountState(e.fsOrch, e.idx, e.client, e.cch, e.tree, testPassphrase)

	rootDir := &Dir{
		state:    state,
		path:     "/",
		treeNode: state.tree,
	}

	opts := &gofuse.Options{
		MountOptions: fuse.MountOptions{
			Name:   "ghfs",
			FsName: "ghfs",
		},
		// Disable all kernel caching so mutations are immediately visible.
		AttrTimeout:  ptrDuration(0),
		EntryTimeout: ptrDuration(0),
	}

	server, err := gofuse.Mount(e.mountpoint, rootDir, opts)
	if err != nil {
		t.Fatalf("FUSE mount failed: %v", err)
	}

	// Run the server in a background goroutine.
	go server.Wait()

	// Poll until the mountpoint is ready.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(e.mountpoint)
		if err == nil {
			// Successfully read the directory; FUSE is mounted.
			_ = entries
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	return server
}

func TestFUSEIntegration(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping FUSE test as root (unprivileged FUSE only)")
	}

	// Check /dev/fuse exists.
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("skipping FUSE test: /dev/fuse not available")
	}

	env := setupFuseEnv(t)

	// Pre-populate a test file before mounting.
	testContent := []byte("hello from ghfs FUSE test")
	env.prepopulateFile(t, "test.txt", testContent)

	server := env.mountFUSE(t)
	defer func() {
		if err := server.Unmount(); err != nil {
			t.Logf("warning: unmount failed: %v", err)
		}
	}()

	t.Run("ReadDir", func(t *testing.T) {
		entries, err := os.ReadDir(env.mountpoint)
		if err != nil {
			t.Fatalf("ReadDir failed: %v", err)
		}

		found := false
		for _, e := range entries {
			if e.Name() == "test.txt" {
				found = true
				if e.IsDir() {
					t.Error("test.txt should be a file, not a directory")
				}
			}
		}
		if !found {
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = e.Name()
			}
			t.Fatalf("test.txt not found in ReadDir, got: %v", names)
		}
	})

	t.Run("ReadFile", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(env.mountpoint, "test.txt"))
		if err != nil {
			t.Fatalf("ReadFile failed: %v", err)
		}
		if string(data) != string(testContent) {
			t.Fatalf("ReadFile content mismatch: got %q, want %q", string(data), string(testContent))
		}
	})

	t.Run("WriteFile", func(t *testing.T) {
		newContent := []byte("newly written via FUSE")
		newPath := filepath.Join(env.mountpoint, "new.txt")

		if err := os.WriteFile(newPath, newContent, 0644); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}

		// Verify the file is readable back through FUSE.
		data, err := os.ReadFile(newPath)
		if err != nil {
			t.Fatalf("ReadFile of new.txt failed: %v", err)
		}
		if string(data) != string(newContent) {
			t.Fatalf("ReadFile new.txt content mismatch: got %q, want %q", string(data), string(newContent))
		}

		// Verify via store that the file was persisted.
		_, _, err = env.idx.GetFile("new.txt")
		if err != nil {
			t.Fatalf("store.GetFile for new.txt failed: %v", err)
		}
	})

	t.Run("Remove", func(t *testing.T) {
		if err := os.Remove(filepath.Join(env.mountpoint, "test.txt")); err != nil {
			t.Fatalf("Remove failed: %v", err)
		}

		entries, err := os.ReadDir(env.mountpoint)
		if err != nil {
			t.Fatalf("ReadDir after remove failed: %v", err)
		}

		for _, e := range entries {
			if e.Name() == "test.txt" {
				t.Fatal("test.txt still listed after removal")
			}
		}
	})

	t.Run("MkdirAndCreate", func(t *testing.T) {
		subdirPath := filepath.Join(env.mountpoint, "subdir")
		if err := os.Mkdir(subdirPath, 0755); err != nil {
			t.Fatalf("Mkdir failed: %v", err)
		}

		nestedContent := []byte("nested file content")
		nestedPath := filepath.Join(subdirPath, "file.txt")
		if err := os.WriteFile(nestedPath, nestedContent, 0644); err != nil {
			t.Fatalf("WriteFile in subdir failed: %v", err)
		}

		// Verify the nested file is readable.
		data, err := os.ReadFile(nestedPath)
		if err != nil {
			t.Fatalf("ReadFile nested file failed: %v", err)
		}
		if string(data) != string(nestedContent) {
			t.Fatalf("nested file content mismatch: got %q, want %q", string(data), string(nestedContent))
		}

		// Verify the subdirectory appears in ReadDir.
		entries, err := os.ReadDir(env.mountpoint)
		if err != nil {
			t.Fatalf("ReadDir root after mkdir failed: %v", err)
		}
		found := false
		for _, e := range entries {
			if e.Name() == "subdir" && e.IsDir() {
				found = true
			}
		}
		if !found {
			t.Fatal("subdir not found in root ReadDir")
		}

		// Verify subdirectory contents.
		subEntries, err := os.ReadDir(subdirPath)
		if err != nil {
			t.Fatalf("ReadDir subdir failed: %v", err)
		}
		if len(subEntries) != 1 || subEntries[0].Name() != "file.txt" {
			names := make([]string, len(subEntries))
			for i, e := range subEntries {
				names[i] = e.Name()
			}
			t.Fatalf("subdir contents unexpected: %v", names)
		}
	})
}
