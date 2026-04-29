package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBearerAuth_NoTokensIsOpen(t *testing.T) {
	mw := BearerAuth(AuthConfig{})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/agents", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("no tokens configured should pass through, got %d", rec.Code)
	}
}

func TestBearerAuth_RejectsMissing(t *testing.T) {
	mw := BearerAuth(AuthConfig{Tokens: []string{"secret"}})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/agents", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != 401 {
		t.Fatalf("missing auth header should be 401, got %d", rec.Code)
	}
}

func TestBearerAuth_RejectsInvalid(t *testing.T) {
	mw := BearerAuth(AuthConfig{Tokens: []string{"secret"}})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/agents", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	h.ServeHTTP(rec, req)

	if rec.Code != 401 {
		t.Fatalf("wrong token should be 401, got %d", rec.Code)
	}
}

func TestBearerAuth_AcceptsValid(t *testing.T) {
	mw := BearerAuth(AuthConfig{Tokens: []string{"secret", "other"}})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))

	for _, tok := range []string{"secret", "other"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/agents", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		h.ServeHTTP(rec, req)
		if rec.Code != 204 {
			t.Fatalf("token %q should be accepted, got %d", tok, rec.Code)
		}
	}
}

func TestBearerAuth_AcceptsBearerCaseInsensitive(t *testing.T) {
	mw := BearerAuth(AuthConfig{Tokens: []string{"secret"}})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/agents", nil)
	req.Header.Set("Authorization", "bearer secret")
	h.ServeHTTP(rec, req)

	if rec.Code != 204 {
		t.Fatalf("lowercase scheme should be accepted, got %d", rec.Code)
	}
}

func TestBearerAuth_SkipPrefixesBypass(t *testing.T) {
	mw := BearerAuth(AuthConfig{
		Tokens:       []string{"secret"},
		SkipPrefixes: []string{"/api/webhooks/", "/health"},
	})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))

	for _, p := range []string{"/api/webhooks/github", "/health"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", p, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != 204 {
			t.Fatalf("%s should bypass auth, got %d", p, rec.Code)
		}
	}
}

func TestBearerAuth_OptionsAlwaysPasses(t *testing.T) {
	mw := BearerAuth(AuthConfig{Tokens: []string{"secret"}})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/api/agents", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("OPTIONS preflight should pass, got %d", rec.Code)
	}
}

func TestVerifyGitHubSignature_Success(t *testing.T) {
	secret := "topsecret"
	body := []byte(`{"ok":true}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	header := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if err := VerifyGitHubSignature(secret, body, header); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
}

func TestVerifyGitHubSignature_NoSecretSkips(t *testing.T) {
	if err := VerifyGitHubSignature("", []byte("body"), ""); err != nil {
		t.Fatalf("empty secret should skip, got %v", err)
	}
}

func TestVerifyGitHubSignature_MissingHeader(t *testing.T) {
	err := VerifyGitHubSignature("secret", []byte("body"), "")
	if err == nil {
		t.Fatal("missing header should error")
	}
}

func TestVerifyGitHubSignature_InvalidHex(t *testing.T) {
	err := VerifyGitHubSignature("secret", []byte("body"), "sha256=zzzz")
	if err == nil {
		t.Fatal("invalid hex should error")
	}
}

func TestVerifyGitHubSignature_WrongDigest(t *testing.T) {
	body := []byte(`{"ok":true}`)
	// hex.EncodeToString of 32 zero bytes is wrong but well-formed.
	header := "sha256=" + strings.Repeat("00", sha256.Size)
	err := VerifyGitHubSignature("secret", body, header)
	if err == nil {
		t.Fatal("wrong digest should error")
	}
}

// TestGitHubWebhook_BadSignature exercises the webhook handler's signature
// rejection path — the handler returns 401 before touching any of the
// downstream dependencies (DB, enqueuer), so no integration setup is needed.
func TestGitHubWebhook_BadSignature(t *testing.T) {
	h := &Handler{GitHubWebhookSecret: "shared-secret"}
	r := h.Routes()

	body := []byte(`{"action":"opened","number":1}`)
	req := httptest.NewRequest("POST", "/api/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != 401 {
		t.Fatalf("bad signature should be 401, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestGitLabWebhook_BadToken does the same for GitLab's shared-token model.
func TestGitLabWebhook_BadToken(t *testing.T) {
	h := &Handler{GitLabWebhookSecret: "shared-secret"}
	r := h.Routes()

	body := []byte(`{"object_kind":"merge_request"}`)
	req := httptest.NewRequest("POST", "/api/webhooks/gitlab", strings.NewReader(string(body)))
	req.Header.Set("X-Gitlab-Token", "wrong")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != 401 {
		t.Fatalf("bad token should be 401, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestVerifyGitLabToken(t *testing.T) {
	if err := VerifyGitLabToken("", "anything"); err != nil {
		t.Fatalf("empty secret should skip, got %v", err)
	}
	if err := VerifyGitLabToken("s3cret", "s3cret"); err != nil {
		t.Fatalf("matching token rejected: %v", err)
	}
	if err := VerifyGitLabToken("s3cret", ""); err == nil {
		t.Fatal("empty header should error when secret set")
	}
	if err := VerifyGitLabToken("s3cret", "wrong"); err == nil {
		t.Fatal("wrong token should error")
	}
}
