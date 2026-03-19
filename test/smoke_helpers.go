//go:build smoke

package integration

import (
	"context"
	"fmt"
	"net/http"
)

func newDeleteRepoRequest(ctx context.Context, token, owner, name string) (*http.Request, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	return req, nil
}

func clientDo(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}
