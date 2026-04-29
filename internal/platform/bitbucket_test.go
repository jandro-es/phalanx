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

func TestBitbucket_FetchDiff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Auth: HTTP Basic with the supplied "user:password" string.
		if u, p, ok := r.BasicAuth(); !ok || u != "alice" || p != "pw" {
			t.Errorf("auth not basic alice:pw — ok=%v u=%q p=%q", ok, u, p)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/diff/aaa..bbb"):
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("diff --git a/foo b/foo\n+added\n-removed\n"))
		case strings.HasSuffix(r.URL.Path, "/diffstat/aaa..bbb"):
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"values": [
					{"type":"diffstat","status":"modified","lines_added":3,"lines_removed":2,
					 "old":{"path":"foo.go"},"new":{"path":"foo.go"}},
					{"type":"diffstat","status":"added","lines_added":7,"lines_removed":0,
					 "new":{"path":"bar.go"}},
					{"type":"diffstat","status":"renamed","lines_added":0,"lines_removed":0,
					 "old":{"path":"old.go"},"new":{"path":"new.go"}}
				]
			}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := NewBitbucketClient("alice:pw", srv.URL)
	result, err := client.FetchDiff(context.Background(), "ws/repo", "bbb", "aaa")
	if err != nil {
		t.Fatalf("FetchDiff: %v", err)
	}
	if !strings.Contains(result.Diff, "+added") {
		t.Errorf("diff missing body: %q", result.Diff)
	}
	if len(result.Files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(result.Files))
	}
	if result.Files[0].Path != "foo.go" || result.Files[0].Additions != 3 || result.Files[0].Deletions != 2 {
		t.Errorf("[0] wrong: %+v", result.Files[0])
	}
	if result.Files[1].Status != "added" || result.Files[1].Additions != 7 {
		t.Errorf("[1] wrong: %+v", result.Files[1])
	}
	if result.Files[2].Status != "renamed" || result.Files[2].OldPath != "old.go" || result.Files[2].Path != "new.go" {
		t.Errorf("[2] rename mapping wrong: %+v", result.Files[2])
	}
}

func TestBitbucket_PostReview_Comment(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/pullrequests/77/comments") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	client := NewBitbucketClient("alice:pw", srv.URL)
	err := client.PostReview(context.Background(),
		types.ReviewSession{RepositoryFullName: "ws/repo", PRNumber: 77},
		types.CompositeReport{Markdown: "# Phalanx"})
	if err != nil {
		t.Fatal(err)
	}
	content, _ := body["content"].(map[string]any)
	if content == nil || content["raw"] != "# Phalanx" {
		t.Errorf("comment body shape wrong: %+v", body)
	}
}

func TestBitbucket_4xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"message":"forbidden"}}`))
	}))
	defer srv.Close()

	client := NewBitbucketClient("u:p", srv.URL)
	_, err := client.FetchDiff(context.Background(), "ws/r", "a", "b")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 error, got %v", err)
	}
}
