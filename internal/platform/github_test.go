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

func TestGitHub_FetchDiff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// All requests should go to the compare endpoint
		if !strings.Contains(r.URL.Path, "/repos/acme/widget/compare/") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("missing or wrong auth: %q", got)
		}

		accept := r.Header.Get("Accept")
		switch accept {
		case "application/vnd.github.v3.diff":
			// Return raw diff
			w.Write([]byte("--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new\n"))
		default:
			// Return file list JSON
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"files": [
					{"filename":"foo.go","status":"modified","additions":1,"deletions":1,"previous_filename":""},
					{"filename":"bar.go","status":"added","additions":20,"deletions":0,"previous_filename":""},
					{"filename":"old.go","status":"renamed","additions":0,"deletions":0,"previous_filename":"baz.go"}
				]
			}`))
		}
	}))
	defer srv.Close()

	client := NewGitHubClient("test-token", srv.URL)
	result, err := client.FetchDiff(context.Background(), "acme/widget", "aaabbb", "cccddd")
	if err != nil {
		t.Fatalf("FetchDiff: %v", err)
	}
	if !strings.Contains(result.Diff, "+new") {
		t.Errorf("diff content not returned: %q", result.Diff)
	}
	if len(result.Files) != 3 {
		t.Fatalf("files: got %d, want 3", len(result.Files))
	}
	// foo.go modified
	if result.Files[0].Path != "foo.go" || result.Files[0].Status != "modified" {
		t.Errorf("files[0] wrong: %+v", result.Files[0])
	}
	// bar.go added with 20 additions
	if result.Files[1].Additions != 20 {
		t.Errorf("files[1].Additions: got %d, want 20", result.Files[1].Additions)
	}
	// renamed file preserves old path
	if result.Files[2].OldPath != "baz.go" {
		t.Errorf("files[2].OldPath: got %q, want baz.go", result.Files[2].OldPath)
	}
}

func TestGitHub_FetchDiff_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	client := NewGitHubClient("test-token", srv.URL)
	_, err := client.FetchDiff(context.Background(), "acme/missing", "a", "b")
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404: %v", err)
	}
}

func TestGitHub_PostReview_CommentAndCheckRun(t *testing.T) {
	var commentBody map[string]any
	var checkRunBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)

		switch {
		case strings.Contains(r.URL.Path, "/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
			_ = json.Unmarshal(raw, &commentBody)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":1}`))
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			_ = json.Unmarshal(raw, &checkRunBody)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":42}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := NewGitHubClient("tok", srv.URL)
	err := client.PostReview(context.Background(), types.ReviewSession{
		RepositoryFullName: "acme/widget",
		PRNumber:           42,
		HeadSHA:            "abc1234",
	}, types.CompositeReport{
		Markdown:       "# Phalanx review\n\nLooks good.",
		OverallVerdict: types.VerdictPass,
	})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}

	if commentBody["body"] != "# Phalanx review\n\nLooks good." {
		t.Errorf("comment body wrong: %v", commentBody)
	}
	if checkRunBody["name"] != "Phalanx Review" {
		t.Errorf("check run name wrong: %v", checkRunBody["name"])
	}
	if checkRunBody["status"] != "completed" {
		t.Errorf("check run status wrong: %v", checkRunBody["status"])
	}
	if checkRunBody["conclusion"] != "success" {
		t.Errorf("check run conclusion for pass should be success, got %v", checkRunBody["conclusion"])
	}
	if checkRunBody["head_sha"] != "abc1234" {
		t.Errorf("check run head_sha wrong: %v", checkRunBody["head_sha"])
	}
}

func TestGitHub_PostReview_VerdictToConclusionMapping(t *testing.T) {
	cases := []struct {
		verdict     types.Verdict
		conclusion  string
	}{
		{types.VerdictPass, "success"},
		{types.VerdictWarn, "neutral"},
		{types.VerdictFail, "failure"},
		{types.VerdictError, "failure"},
	}

	for _, c := range cases {
		t.Run(string(c.verdict), func(t *testing.T) {
			var seen map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if strings.HasSuffix(r.URL.Path, "/check-runs") {
					_ = json.Unmarshal(raw, &seen)
				}
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(`{"id":1}`))
			}))
			defer srv.Close()

			client := NewGitHubClient("tok", srv.URL)
			_ = client.PostReview(context.Background(), types.ReviewSession{
				RepositoryFullName: "acme/w",
				PRNumber:           1,
				HeadSHA:            "a",
			}, types.CompositeReport{
				Markdown:       "# hi",
				OverallVerdict: c.verdict,
			})
			if seen["conclusion"] != c.conclusion {
				t.Errorf("verdict %s → got conclusion %v, want %s", c.verdict, seen["conclusion"], c.conclusion)
			}
		})
	}
}

func TestGitHub_VerifyUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer usertoken" {
			t.Errorf("auth header: got %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"id":12345,"login":"alice","email":"alice@example.com","avatar_url":"https://..."}`))
	}))
	defer srv.Close()

	client := NewGitHubClient("tok", srv.URL)
	user, err := client.VerifyUser(context.Background(), "usertoken")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != "12345" {
		t.Errorf("ID: got %q, want 12345", user.ID)
	}
	if user.Login != "alice" {
		t.Errorf("Login: got %q", user.Login)
	}
	if user.Email != "alice@example.com" {
		t.Errorf("Email: got %q", user.Email)
	}
}

func TestGitHub_TrailingSlashBaseURLIsTrimmed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Must not see a double slash
		if strings.HasPrefix(r.URL.Path, "//") {
			t.Errorf("double slash: %q", r.URL.Path)
		}
		w.Write([]byte(`{"files":[]}`))
	}))
	defer srv.Close()

	client := NewGitHubClient("tok", srv.URL+"/")
	_, err := client.FetchDiff(context.Background(), "a/b", "x", "y")
	if err != nil {
		t.Fatal(err)
	}
}
