// Package github provides a client for the GitHub Contents API used by ghfs.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ContentAPI defines the operations ghfs needs against GitHub's Contents API.
type ContentAPI interface {
	PutFile(ctx context.Context, repo, path string, content []byte) (sha string, err error)
	GetFile(ctx context.Context, repo, path string) ([]byte, error)
	DeleteFile(ctx context.Context, repo, path, sha string) error
	CreateRepo(ctx context.Context, name string, private bool) error
	RepoExists(ctx context.Context, name string) (bool, error)
}

// RateLimitError is returned when GitHub responds with 403 and
// X-RateLimit-Remaining is 0.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("github: rate limited, retry after %s", e.RetryAfter)
}

// Client implements ContentAPI using GitHub's REST API and the standard library.
type Client struct {
	token      string
	owner      string
	httpClient *http.Client
	baseURL    string
}

// NewClient creates a Client targeting https://api.github.com.
func NewClient(token, owner string) *Client {
	return NewClientWithBaseURL(token, owner, "https://api.github.com")
}

// NewClientWithBaseURL creates a Client with a custom API base URL.
func NewClientWithBaseURL(token, owner, baseURL string) *Client {
	return &Client{
		token:      token,
		owner:      owner,
		httpClient: &http.Client{},
		baseURL:    baseURL,
	}
}

// contentsResponse is the shape returned by the GitHub Contents API.
type contentsResponse struct {
	SHA     string `json:"sha"`
	Content string `json:"content"`
	GitURL  string `json:"git_url"`
}

// blobResponse is the shape returned by the GitHub Git Blobs API.
type blobResponse struct {
	Content string `json:"content"`
}

// putFileRequest is the request body for creating/updating a file.
type putFileRequest struct {
	Message string `json:"message"`
	Content string `json:"content"`
	SHA     string `json:"sha,omitempty"`
}

// putFileResponse is the response from creating/updating a file.
type putFileResponse struct {
	Content struct {
		SHA string `json:"sha"`
	} `json:"content"`
}

// deleteFileRequest is the request body for deleting a file.
type deleteFileRequest struct {
	Message string `json:"message"`
	SHA     string `json:"sha"`
}

// createRepoRequest is the request body for creating a repository.
type createRepoRequest struct {
	Name    string `json:"name"`
	Private bool   `json:"private"`
}

// doRequest executes an HTTP request with standard GitHub headers and checks
// for rate-limit errors. It returns the response body bytes and the status code.
func (c *Client) doRequest(ctx context.Context, method, url string, body io.Reader) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, 0, fmt.Errorf("github: creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("github: executing request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("github: reading response: %w", err)
	}

	// Check for rate limiting.
	if resp.StatusCode == http.StatusForbidden {
		remaining := resp.Header.Get("X-RateLimit-Remaining")
		if remaining == "0" {
			resetStr := resp.Header.Get("X-RateLimit-Reset")
			var retryAfter time.Duration
			if resetStr != "" {
				resetUnix, parseErr := strconv.ParseInt(resetStr, 10, 64)
				if parseErr == nil {
					retryAfter = time.Until(time.Unix(resetUnix, 0))
					if retryAfter < 0 {
						retryAfter = 0
					}
				}
			}
			return nil, resp.StatusCode, &RateLimitError{RetryAfter: retryAfter}
		}
	}

	return respBody, resp.StatusCode, nil
}

// PutFile creates or updates a file in the repository via the Contents API.
// If the file already exists, it fetches the current SHA first for an update.
func (c *Client) PutFile(ctx context.Context, repo, path string, content []byte) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s", c.baseURL, c.owner, repo, path)

	reqBody := putFileRequest{
		Message: "ghfs: update",
		Content: base64.StdEncoding.EncodeToString(content),
	}

	// Try to get existing file SHA for updates.
	existingBody, status, err := c.doRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		// If it's a rate limit error, propagate it.
		if _, ok := err.(*RateLimitError); ok {
			return "", err
		}
		// For other errors on GET, assume file doesn't exist yet.
	} else if status == http.StatusOK {
		var existing contentsResponse
		if jsonErr := json.Unmarshal(existingBody, &existing); jsonErr == nil {
			reqBody.SHA = existing.SHA
		}
	}

	bodyBytes, marshalErr := json.Marshal(reqBody)
	if marshalErr != nil {
		return "", fmt.Errorf("github: marshaling put request: %w", marshalErr)
	}

	respBody, status, err := c.doRequest(ctx, http.MethodPut, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return "", fmt.Errorf("github: PutFile unexpected status %d: %s", status, string(respBody))
	}

	var putResp putFileResponse
	if err := json.Unmarshal(respBody, &putResp); err != nil {
		return "", fmt.Errorf("github: unmarshaling put response: %w", err)
	}

	return putResp.Content.SHA, nil
}

// GetFile retrieves a file's content from the repository. If the file is larger
// than 1 MB (content field empty), it follows the git_url to the Blobs API.
func (c *Client) GetFile(ctx context.Context, repo, path string) ([]byte, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s", c.baseURL, c.owner, repo, path)

	respBody, status, err := c.doRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("github: GetFile unexpected status %d: %s", status, string(respBody))
	}

	var cr contentsResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return nil, fmt.Errorf("github: unmarshaling contents response: %w", err)
	}

	// If content is present, decode and return.
	if cr.Content != "" {
		decoded, err := base64.StdEncoding.DecodeString(cr.Content)
		if err != nil {
			return nil, fmt.Errorf("github: decoding base64 content: %w", err)
		}
		return decoded, nil
	}

	// Content empty (file >1MB) — follow git_url to Blobs API.
	if cr.GitURL == "" {
		return nil, fmt.Errorf("github: GetFile: no content and no git_url")
	}

	blobBody, blobStatus, err := c.doRequest(ctx, http.MethodGet, cr.GitURL, nil)
	if err != nil {
		return nil, err
	}
	if blobStatus != http.StatusOK {
		return nil, fmt.Errorf("github: GetFile blob unexpected status %d: %s", blobStatus, string(blobBody))
	}

	var br blobResponse
	if err := json.Unmarshal(blobBody, &br); err != nil {
		return nil, fmt.Errorf("github: unmarshaling blob response: %w", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(br.Content)
	if err != nil {
		return nil, fmt.Errorf("github: decoding blob base64 content: %w", err)
	}
	return decoded, nil
}

// DeleteFile removes a file from the repository via the Contents API.
func (c *Client) DeleteFile(ctx context.Context, repo, path, sha string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s", c.baseURL, c.owner, repo, path)

	reqBody := deleteFileRequest{
		Message: "ghfs: delete",
		SHA:     sha,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("github: marshaling delete request: %w", err)
	}

	respBody, status, err := c.doRequest(ctx, http.MethodDelete, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("github: DeleteFile unexpected status %d: %s", status, string(respBody))
	}

	return nil
}

// CreateRepo creates a new repository for the authenticated user.
func (c *Client) CreateRepo(ctx context.Context, name string, private bool) error {
	url := fmt.Sprintf("%s/user/repos", c.baseURL)

	reqBody := createRepoRequest{
		Name:    name,
		Private: private,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("github: marshaling create repo request: %w", err)
	}

	respBody, status, err := c.doRequest(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	if status != http.StatusCreated {
		return fmt.Errorf("github: CreateRepo unexpected status %d: %s", status, string(respBody))
	}

	return nil
}

// RepoExists checks whether a repository exists. Returns true on 200, false on 404.
func (c *Client) RepoExists(ctx context.Context, name string) (bool, error) {
	url := fmt.Sprintf("%s/repos/%s/%s", c.baseURL, c.owner, name)

	_, status, err := c.doRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}

	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("github: RepoExists unexpected status %d", status)
	}
}

// Compile-time check that Client implements ContentAPI.
var _ ContentAPI = (*Client)(nil)
