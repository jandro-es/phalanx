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
		files = append(files, types.FileEntry{Path: d.NewPath, Status: status})
	}

	return &DiffResult{Diff: strings.Join(diffParts, "\n"), Files: files}, nil
}

func (gl *GitLabClient) PostReview(ctx context.Context, session types.ReviewSession, report types.CompositeReport) error {
	projID := strings.ReplaceAll(session.RepositoryFullName, "/", "%2F")
	_, err := gl.request(ctx, "POST",
		fmt.Sprintf("/projects/%s/merge_requests/%d/notes", projID, session.PRNumber),
		map[string]string{"body": report.Markdown})
	return err
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
