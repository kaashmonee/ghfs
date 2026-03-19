package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPutFile_Create(t *testing.T) {
	fileContent := []byte("hello ghfs")
	// PutFile double base64-encodes: inner = base64(raw), outer = base64(inner) for API content field
	innerB64 := base64.StdEncoding.EncodeToString(fileContent)
	expectedB64 := base64.StdEncoding.EncodeToString([]byte(innerB64))

	var gotMethod, gotPath string
	var gotBody putFileRequest

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(putFileResponse{
			Content: struct {
				SHA string `json:"sha"`
			}{SHA: "abc123"},
		})
	}))
	defer ts.Close()

	c := NewClientWithBaseURL("tok", "owner", ts.URL)
	sha, err := c.PutFile(context.Background(), "repo", "dir/file.txt", fileContent)
	if err != nil {
		t.Fatalf("PutFile returned error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("expected PUT, got %s", gotMethod)
	}
	if gotPath != "/repos/owner/repo/contents/dir/file.txt" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotBody.Content != expectedB64 {
		t.Errorf("expected base64 %q, got %q", expectedB64, gotBody.Content)
	}
	if gotBody.SHA != "" {
		t.Errorf("expected empty SHA for create, got %q", gotBody.SHA)
	}
	if sha != "abc123" {
		t.Errorf("expected sha abc123, got %q", sha)
	}
}

func TestGetFile(t *testing.T) {
	original := []byte("file content here")
	// Simulate what GitHub stores: PutFile sends double-encoded content.
	// GitHub base64-decodes the API content field, storing the inner base64 string.
	// On GET, GitHub base64-encodes that stored string, giving us the outer layer.
	innerB64 := base64.StdEncoding.EncodeToString(original)
	storedAsB64 := base64.StdEncoding.EncodeToString([]byte(innerB64))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(contentsResponse{
			SHA:     "sha1",
			Content: storedAsB64,
		})
	}))
	defer ts.Close()

	c := NewClientWithBaseURL("tok", "owner", ts.URL)
	got, err := c.GetFile(context.Background(), "repo", "file.txt")
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("expected %q, got %q", original, got)
	}
}

func TestGetFile_LargeFile(t *testing.T) {
	original := []byte("large file content")
	innerB64 := base64.StdEncoding.EncodeToString(original)
	storedAsB64 := base64.StdEncoding.EncodeToString([]byte(innerB64))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/contents/big.bin" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(contentsResponse{
				SHA:    "sha1",
				GitURL: "http://" + r.Host + "/repos/owner/repo/git/blobs/sha1",
			})
			return
		}
		// Blobs API returns the stored content base64-encoded.
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(blobResponse{Content: storedAsB64})
	}))
	defer ts.Close()

	c := NewClientWithBaseURL("tok", "owner", ts.URL)
	got, err := c.GetFile(context.Background(), "repo", "big.bin")
	if err != nil {
		t.Fatalf("GetFile (large) returned error: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("expected %q, got %q", original, got)
	}
}

func TestDeleteFile(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody deleteFileRequest

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
	}))
	defer ts.Close()

	c := NewClientWithBaseURL("tok", "owner", ts.URL)
	err := c.DeleteFile(context.Background(), "repo", "file.txt", "deadbeef")
	if err != nil {
		t.Fatalf("DeleteFile returned error: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("expected DELETE, got %s", gotMethod)
	}
	if gotPath != "/repos/owner/repo/contents/file.txt" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotBody.SHA != "deadbeef" {
		t.Errorf("expected sha deadbeef, got %q", gotBody.SHA)
	}
	if gotBody.Message != "ghfs: delete" {
		t.Errorf("expected message 'ghfs: delete', got %q", gotBody.Message)
	}
}

func TestCreateRepo(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody createRepoRequest

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("{}"))
	}))
	defer ts.Close()

	c := NewClientWithBaseURL("tok", "owner", ts.URL)
	err := c.CreateRepo(context.Background(), "my-repo", true)
	if err != nil {
		t.Fatalf("CreateRepo returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/user/repos" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotBody.Name != "my-repo" {
		t.Errorf("expected name my-repo, got %q", gotBody.Name)
	}
	if !gotBody.Private {
		t.Error("expected private=true")
	}
}

func TestRepoExists_True(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/existing" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{}"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	c := NewClientWithBaseURL("tok", "owner", ts.URL)

	exists, err := c.RepoExists(context.Background(), "existing")
	if err != nil {
		t.Fatalf("RepoExists returned error: %v", err)
	}
	if !exists {
		t.Error("expected true for existing repo")
	}
}

func TestRepoExists_False(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	c := NewClientWithBaseURL("tok", "owner", ts.URL)

	exists, err := c.RepoExists(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("RepoExists returned error: %v", err)
	}
	if exists {
		t.Error("expected false for nonexistent repo")
	}
}

func TestRateLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"rate limit exceeded"}`))
	}))
	defer ts.Close()

	c := NewClientWithBaseURL("tok", "owner", ts.URL)

	_, err := c.GetFile(context.Background(), "repo", "file.txt")
	if err == nil {
		t.Fatal("expected error for rate-limited response")
	}

	rlErr, ok := err.(*RateLimitError)
	if !ok {
		t.Fatalf("expected *RateLimitError, got %T: %v", err, err)
	}
	// The RetryAfter should be set (could be negative since the reset time is in the past,
	// but our code clamps to 0).
	if rlErr.RetryAfter < 0 {
		t.Errorf("expected non-negative RetryAfter, got %v", rlErr.RetryAfter)
	}
}

func TestDoRequest_SetsHeaders(t *testing.T) {
	var gotAuth, gotAccept string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
	}))
	defer ts.Close()

	c := NewClientWithBaseURL("my-token", "owner", ts.URL)
	c.doRequest(context.Background(), http.MethodGet, ts.URL+"/test", nil)

	if gotAuth != "Bearer my-token" {
		t.Errorf("expected 'Bearer my-token', got %q", gotAuth)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Errorf("expected 'application/vnd.github+json', got %q", gotAccept)
	}
}
