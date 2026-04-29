package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/phalanx-ai/phalanx/internal/types"
)

func TestGitLab_FetchDiff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Token auth
		if r.Header.Get("PRIVATE-TOKEN") != "glpat-test" {
			t.Errorf("auth header: got %q", r.Header.Get("PRIVATE-TOKEN"))
		}
		// Project path must be URL-encoded on the wire. RawPath carries
		// the on-wire representation; Path is the decoded form.
		rawPath := r.URL.RawPath
		if rawPath == "" {
			rawPath = r.URL.Path
		}
		if !strings.Contains(rawPath, "/projects/acme%2Fwidget/") {
			t.Errorf("project path not URL-encoded: %q", rawPath)
		}
		// Compare endpoint
		if !strings.Contains(r.URL.Path, "/repository/compare") {
			t.Errorf("unexpected path: %q", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"diffs": [
				{
					"diff": "--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new\n",
					"new_path": "foo.go",
					"old_path": "foo.go",
					"new_file": false,
					"deleted_file": false,
					"renamed_file": false
				},
				{
					"diff": "+new contents\n",
					"new_path": "bar.go",
					"old_path": "bar.go",
					"new_file": true
				},
				{
					"diff": "",
					"new_path": "baz_new.go",
					"old_path": "baz_old.go",
					"renamed_file": true
				}
			]
		}`))
	}))
	defer srv.Close()

	client := NewGitLabClient("glpat-test", srv.URL)
	result, err := client.FetchDiff(context.Background(), "acme/widget", "aaa", "bbb")
	if err != nil {
		t.Fatalf("FetchDiff: %v", err)
	}
	if !strings.Contains(result.Diff, "+new") {
		t.Errorf("diff not concatenated: %q", result.Diff)
	}
	if len(result.Files) != 3 {
		t.Fatalf("files: got %d, want 3", len(result.Files))
	}
	// modified file
	if result.Files[0].Status != "modified" {
		t.Errorf("[0] status: got %q, want modified", result.Files[0].Status)
	}
	// new file
	if result.Files[1].Status != "added" {
		t.Errorf("[1] status: got %q, want added", result.Files[1].Status)
	}
	// renamed file
	if result.Files[2].Status != "renamed" {
		t.Errorf("[2] status: got %q, want renamed", result.Files[2].Status)
	}

	// P1.5: GitLab compare doesn't return additions/deletions, so the
	// platform client must compute them from the diff hunk so the composite
	// report's "Changed Files (+X/-Y)" line is non-zero.
	if result.Files[0].Additions != 1 || result.Files[0].Deletions != 1 {
		t.Errorf("[0] add/del: got +%d/-%d, want +1/-1",
			result.Files[0].Additions, result.Files[0].Deletions)
	}
	if result.Files[1].Additions != 1 || result.Files[1].Deletions != 0 {
		t.Errorf("[1] add/del: got +%d/-%d, want +1/-0",
			result.Files[1].Additions, result.Files[1].Deletions)
	}
	// renamed-only file has empty diff → both should be zero.
	if result.Files[2].Additions != 0 || result.Files[2].Deletions != 0 {
		t.Errorf("[2] add/del should be 0/0, got +%d/-%d",
			result.Files[2].Additions, result.Files[2].Deletions)
	}
	// Renames also need OldPath populated so the report can show "old → new".
	if result.Files[2].OldPath != "baz_old.go" {
		t.Errorf("[2] old_path: got %q, want baz_old.go", result.Files[2].OldPath)
	}
}

func TestCountDiffLines(t *testing.T) {
	cases := []struct {
		name        string
		hunk        string
		wantAdd     int
		wantDel     int
	}{
		{
			"basic +/- lines",
			"@@ -1,3 +1,3 @@\n line one\n-old\n+new\n line three\n",
			1, 1,
		},
		{
			"file headers ignored",
			"--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new\n",
			1, 1,
		},
		{
			"empty hunk",
			"",
			0, 0,
		},
		{
			"only additions",
			"+a\n+b\n+c\n",
			3, 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotAdd, gotDel := countDiffLines(c.hunk)
			if gotAdd != c.wantAdd || gotDel != c.wantDel {
				t.Errorf("got +%d/-%d, want +%d/-%d", gotAdd, gotDel, c.wantAdd, c.wantDel)
			}
		})
	}
}

func TestGitLab_PostReview_Note(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/merge_requests/42/notes") {
			t.Errorf("path: got %q", r.URL.Path)
		}
		rawPath := r.URL.RawPath
		if rawPath == "" {
			rawPath = r.URL.Path
		}
		if !strings.Contains(rawPath, "/projects/acme%2Fwidget/") {
			t.Errorf("project not URL-encoded: %q", rawPath)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	client := NewGitLabClient("tok", srv.URL)
	err := client.PostReview(context.Background(), types.ReviewSession{
		RepositoryFullName: "acme/widget",
		PRNumber:           42,
	}, types.CompositeReport{
		Markdown: "# Phalanx\n\nLGTM.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["body"] != "# Phalanx\n\nLGTM." {
		t.Errorf("note body wrong: %v", body["body"])
	}
}

func TestGitLab_VerifyUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/user" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		if r.Header.Get("PRIVATE-TOKEN") != "user-token" {
			t.Errorf("token header: got %q", r.Header.Get("PRIVATE-TOKEN"))
		}
		w.Write([]byte(`{"id":7,"username":"bob","email":"bob@example.com","avatar_url":""}`))
	}))
	defer srv.Close()

	client := NewGitLabClient("tok", srv.URL)
	user, err := client.VerifyUser(context.Background(), "user-token")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != "7" || user.Login != "bob" {
		t.Errorf("user wrong: %+v", user)
	}
}

func TestGitLab_BaseURLAppendsAPIv4(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The constructor appends /api/v4 to the base URL, so requests
		// must include that segment.
		if !strings.HasPrefix(r.URL.Path, "/api/v4/") {
			t.Errorf("path missing /api/v4 prefix: %q", r.URL.Path)
		}
		w.Write([]byte(`{"diffs":[]}`))
	}))
	defer srv.Close()

	client := NewGitLabClient("tok", srv.URL)
	if _, err := client.FetchDiff(context.Background(), "a/b", "x", "y"); err != nil {
		t.Fatal(err)
	}
}

func TestGitLab_5xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message":"boom"}`))
	}))
	defer srv.Close()

	client := NewGitLabClient("tok", srv.URL)
	_, err := client.FetchDiff(context.Background(), "a/b", "x", "y")
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500: %v", err)
	}
}
