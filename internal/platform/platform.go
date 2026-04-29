// Package platform provides Git platform integrations (GitHub, GitLab).
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/phalanx-ai/phalanx/internal/types"
)

// DiffResult contains the diff and file list for a PR.
type DiffResult struct {
	Diff  string
	Files []types.FileEntry
}

// Client is the interface for Git platform operations.
type Client interface {
	FetchDiff(ctx context.Context, repo, baseSHA, headSHA string) (*DiffResult, error)
	PostReview(ctx context.Context, session types.ReviewSession, report types.CompositeReport) error
	VerifyUser(ctx context.Context, token string) (*UserInfo, error)
}

// UserInfo is the identity of a verified user.
type UserInfo struct {
	ID        string `json:"id"`
	Login     string `json:"login"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatarUrl"`
}

// ==========================================================================
// GitHub Client
// ==========================================================================

// GitHubClient implements Client for GitHub.
type GitHubClient struct {
	token   string
	baseURL string
	client  *http.Client
}

// NewGitHubClient creates a GitHub client.
func NewGitHubClient(token, baseURL string) *GitHubClient {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &GitHubClient{token: token, baseURL: strings.TrimRight(baseURL, "/"), client: &http.Client{}}
}

func (g *GitHubClient) request(ctx context.Context, method, path string, body any, accept string) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, g.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	if accept != "" {
		req.Header.Set("Accept", accept)
	} else {
		req.Header.Set("Accept", "application/vnd.github.v3+json")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("github %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

func (g *GitHubClient) FetchDiff(ctx context.Context, repo, baseSHA, headSHA string) (*DiffResult, error) {
	// Get diff
	diffData, err := g.request(ctx, "GET",
		fmt.Sprintf("/repos/%s/compare/%s...%s", repo, baseSHA, headSHA),
		nil, "application/vnd.github.v3.diff")
	if err != nil {
		return nil, err
	}

	// Get file list
	compareData, err := g.request(ctx, "GET",
		fmt.Sprintf("/repos/%s/compare/%s...%s", repo, baseSHA, headSHA), nil, "")
	if err != nil {
		return nil, err
	}

	var compare struct {
		Files []struct {
			Filename         string `json:"filename"`
			Status           string `json:"status"`
			Additions        int    `json:"additions"`
			Deletions        int    `json:"deletions"`
			PreviousFilename string `json:"previous_filename"`
		} `json:"files"`
	}
	json.Unmarshal(compareData, &compare)

	files := make([]types.FileEntry, len(compare.Files))
	for i, f := range compare.Files {
		files[i] = types.FileEntry{
			Path: f.Filename, Status: f.Status,
			Additions: f.Additions, Deletions: f.Deletions,
			OldPath: f.PreviousFilename,
		}
	}

	return &DiffResult{Diff: string(diffData), Files: files}, nil
}

func (g *GitHubClient) PostReview(ctx context.Context, session types.ReviewSession, report types.CompositeReport) error {
	// Post comment
	_, err := g.request(ctx, "POST",
		fmt.Sprintf("/repos/%s/issues/%d/comments", session.RepositoryFullName, session.PRNumber),
		map[string]string{"body": report.Markdown}, "")
	if err != nil {
		return err
	}

	// Create check run
	conclusion := "success"
	if report.OverallVerdict == types.VerdictFail || report.OverallVerdict == types.VerdictError {
		conclusion = "failure"
	} else if report.OverallVerdict == types.VerdictWarn {
		conclusion = "neutral"
	}

	_, err = g.request(ctx, "POST",
		fmt.Sprintf("/repos/%s/check-runs", session.RepositoryFullName),
		map[string]any{
			"name":       "Phalanx Review",
			"head_sha":   session.HeadSHA,
			"status":     "completed",
			"conclusion": conclusion,
			"output": map[string]string{
				"title":   fmt.Sprintf("Phalanx: %s", strings.ToUpper(string(report.OverallVerdict))),
				"summary": fmt.Sprintf("Review completed: **%s**", report.OverallVerdict),
			},
		}, "")
	return err
}

func (g *GitHubClient) VerifyUser(ctx context.Context, token string) (*UserInfo, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", g.baseURL+"/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var user struct {
		ID        int    `json:"id"`
		Login     string `json:"login"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	json.NewDecoder(resp.Body).Decode(&user)

	return &UserInfo{
		ID: fmt.Sprintf("%d", user.ID), Login: user.Login,
		Email: user.Email, AvatarURL: user.AvatarURL,
	}, nil
}

// ==========================================================================
// GitLab Client
// ==========================================================================

// GitLabClient implements Client for GitLab.
type GitLabClient struct {
	token   string
	baseURL string
	client  *http.Client
}

// NewGitLabClient creates a GitLab client.
func NewGitLabClient(token, baseURL string) *GitLabClient {
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}
	return &GitLabClient{
		token:   token,
		baseURL: strings.TrimRight(baseURL, "/") + "/api/v4",
		client:  &http.Client{},
	}
}

func (gl *GitLabClient) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, gl.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", gl.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := gl.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("gitlab %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

func (gl *GitLabClient) FetchDiff(ctx context.Context, repo, baseSHA, headSHA string) (*DiffResult, error) {
	projID := strings.ReplaceAll(repo, "/", "%2F")
	data, err := gl.request(ctx, "GET",
		fmt.Sprintf("/projects/%s/repository/compare?from=%s&to=%s", projID, baseSHA, headSHA), nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Diffs []struct {
			Diff        string `json:"diff"`
			NewPath     string `json:"new_path"`
			OldPath     string `json:"old_path"`
			NewFile     bool   `json:"new_file"`
			DeletedFile bool   `json:"deleted_file"`
			RenamedFile bool   `json:"renamed_file"`
		} `json:"diffs"`
	}
	json.Unmarshal(data, &result)

	var diffParts []string
	var files []types.FileEntry
	for _, d := range result.Diffs {
		diffParts = append(diffParts, d.Diff)
		status := "modified"
		if d.NewFile {
			status = "added"
		} else if d.DeletedFile {
			status = "deleted"
		} else if d.RenamedFile {
			status = "renamed"
		}
		add, del := countDiffLines(d.Diff)
		files = append(files, types.FileEntry{
			Path:      d.NewPath,
			OldPath:   d.OldPath,
			Status:    status,
			Additions: add,
			Deletions: del,
		})
	}

	return &DiffResult{Diff: strings.Join(diffParts, "\n"), Files: files}, nil
}

// countDiffLines tallies "+" and "-" content lines in a unified diff hunk,
// excluding the file headers (`+++`, `---`). GitLab's compare API doesn't
// return per-file add/delete counts, so we compute them here so the composite
// report's "Changed Files (+X/-Y)" line is non-zero on GitLab.
func countDiffLines(hunk string) (additions, deletions int) {
	for _, line := range strings.Split(hunk, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			// File markers — skip.
		case strings.HasPrefix(line, "+"):
			additions++
		case strings.HasPrefix(line, "-"):
			deletions++
		}
	}
	return additions, deletions
}

func (gl *GitLabClient) PostReview(ctx context.Context, session types.ReviewSession, report types.CompositeReport) error {
	projID := strings.ReplaceAll(session.RepositoryFullName, "/", "%2F")
	_, err := gl.request(ctx, "POST",
		fmt.Sprintf("/projects/%s/merge_requests/%d/notes", projID, session.PRNumber),
		map[string]string{"body": report.Markdown})
	return err
}

// ==========================================================================
// Bitbucket Cloud Client
// ==========================================================================

// BitbucketClient implements Client for Bitbucket Cloud (bitbucket.org).
//
// Auth uses HTTP Basic with `<username>:<app_password>` — Bitbucket Cloud
// doesn't support bearer tokens for the v2.0 REST API. The constructor takes
// the raw "user:password" string (or "x-token-auth:<workspace_token>" if
// using a workspace access token).
type BitbucketClient struct {
	auth    string // base "user:password" string before base64 encoding
	baseURL string
	client  *http.Client
}

// NewBitbucketClient creates a Bitbucket Cloud client. baseURL defaults to
// the public API; pass a custom value for Bitbucket Server / DC (which
// requires a different API path layout — not yet supported).
func NewBitbucketClient(auth, baseURL string) *BitbucketClient {
	if baseURL == "" {
		baseURL = "https://api.bitbucket.org/2.0"
	}
	return &BitbucketClient{
		auth:    auth,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{},
	}
}

func (b *BitbucketClient) request(ctx context.Context, method, path string, body any, accept string) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if b.auth != "" {
		// HTTP Basic. Bitbucket also accepts the encoded form directly.
		parts := strings.SplitN(b.auth, ":", 2)
		if len(parts) == 2 {
			req.SetBasicAuth(parts[0], parts[1])
		}
	}
	if accept == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("bitbucket %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// FetchDiff hits Bitbucket's /diff (raw text) and /diffstat (per-file stats)
// endpoints in parallel-via-sequential calls so the report has both the diff
// body and accurate +/- counts. `repo` is "<workspace>/<repo_slug>".
func (b *BitbucketClient) FetchDiff(ctx context.Context, repo, baseSHA, headSHA string) (*DiffResult, error) {
	spec := fmt.Sprintf("%s..%s", headSHA, baseSHA)

	diffData, err := b.request(ctx, "GET",
		fmt.Sprintf("/repositories/%s/diff/%s", repo, spec),
		nil, "text/plain")
	if err != nil {
		return nil, err
	}

	statData, err := b.request(ctx, "GET",
		fmt.Sprintf("/repositories/%s/diffstat/%s", repo, spec),
		nil, "")
	if err != nil {
		return nil, err
	}

	var stat struct {
		Values []struct {
			Type             string `json:"type"`
			Status           string `json:"status"`
			LinesAdded       int    `json:"lines_added"`
			LinesRemoved     int    `json:"lines_removed"`
			Old              *struct{ Path string } `json:"old"`
			New              *struct{ Path string } `json:"new"`
		} `json:"values"`
	}
	_ = json.Unmarshal(statData, &stat)

	files := make([]types.FileEntry, 0, len(stat.Values))
	for _, v := range stat.Values {
		fe := types.FileEntry{
			Status:    bitbucketStatus(v.Status),
			Additions: v.LinesAdded,
			Deletions: v.LinesRemoved,
		}
		if v.New != nil {
			fe.Path = v.New.Path
		}
		if v.Old != nil {
			fe.OldPath = v.Old.Path
			if fe.Path == "" {
				fe.Path = v.Old.Path
			}
		}
		files = append(files, fe)
	}

	return &DiffResult{Diff: string(diffData), Files: files}, nil
}

// bitbucketStatus normalises Bitbucket's diffstat status values onto the
// shared FileEntry vocabulary used by the renderer.
func bitbucketStatus(s string) string {
	switch s {
	case "added":
		return "added"
	case "removed":
		return "deleted"
	case "renamed":
		return "renamed"
	default:
		return "modified"
	}
}

// PostReview posts the composite report as a PR comment. Bitbucket doesn't
// have an equivalent of GitHub Check Runs, so the verdict is encoded in the
// comment body only.
func (b *BitbucketClient) PostReview(ctx context.Context, session types.ReviewSession, report types.CompositeReport) error {
	_, err := b.request(ctx, "POST",
		fmt.Sprintf("/repositories/%s/pullrequests/%d/comments", session.RepositoryFullName, session.PRNumber),
		map[string]any{
			"content": map[string]string{"raw": report.Markdown},
		}, "")
	return err
}

// VerifyUser identifies the caller via /user using the supplied
// "username:app_password" string (overriding the client default).
func (b *BitbucketClient) VerifyUser(ctx context.Context, token string) (*UserInfo, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", b.baseURL+"/user", nil)
	if token != "" {
		parts := strings.SplitN(token, ":", 2)
		if len(parts) == 2 {
			req.SetBasicAuth(parts[0], parts[1])
		}
	}
	req.Header.Set("Accept", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var user struct {
		AccountID   string `json:"account_id"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Links       struct {
			Avatar struct{ Href string } `json:"avatar"`
		} `json:"links"`
	}
	json.NewDecoder(resp.Body).Decode(&user)

	return &UserInfo{
		ID:        user.AccountID,
		Login:     user.Username,
		AvatarURL: user.Links.Avatar.Href,
	}, nil
}

func (gl *GitLabClient) VerifyUser(ctx context.Context, token string) (*UserInfo, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", gl.baseURL+"/user", nil)
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := gl.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var user struct {
		ID        int    `json:"id"`
		Username  string `json:"username"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	json.NewDecoder(resp.Body).Decode(&user)

	return &UserInfo{
		ID: fmt.Sprintf("%d", user.ID), Login: user.Username,
		Email: user.Email, AvatarURL: user.AvatarURL,
	}, nil
}
