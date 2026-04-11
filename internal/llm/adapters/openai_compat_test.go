package adapters

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

func openaiProvider(baseURL string, auth types.AuthMethod) types.LLMProvider {
	key := "env://TEST_OPENAI_KEY"
	return types.LLMProvider{
		Name:         "openai",
		BaseURL:      baseURL,
		AuthMethod:   auth,
		APIKeyRef:    &key,
		DefaultModel: "gpt-4.1",
	}
}

func TestOpenAI_HappyPath(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY","sk-openai-test")

	var seenHeaders http.Header
	var seenPath string
	var seenBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenHeaders = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &seenBody)

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"chatcmpl-1",
			"model":"gpt-4.1-2025",
			"choices":[{
				"message":{"role":"assistant","content":"OK."},
				"finish_reason":"stop"
			}],
			"usage":{"prompt_tokens":12,"completion_tokens":3}
		}`))
	}))
	defer srv.Close()

	resp, err := NewOpenAICompatAdapter().Complete(context.Background(), types.LLMRequest{
		Model: "gpt-4.1",
		Messages: []types.LLMMessage{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hi."},
		},
		Temperature: 0,
		MaxTokens:   256,
	}, openaiProvider(srv.URL, types.AuthBearer))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if resp.Content != "OK." {
		t.Errorf("content: got %q", resp.Content)
	}
	if resp.InputTokens != 12 || resp.OutputTokens != 3 {
		t.Errorf("tokens: in=%d out=%d", resp.InputTokens, resp.OutputTokens)
	}
	if resp.Model != "gpt-4.1-2025" {
		t.Errorf("model echo: %q", resp.Model)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("finish reason: %q", resp.FinishReason)
	}

	// Path: adapter should append /v1/chat/completions when base_url doesn't already end with /v1
	if !strings.HasSuffix(seenPath, "/chat/completions") {
		t.Errorf("path: got %q, want to end with /chat/completions", seenPath)
	}

	// Bearer auth
	if seenHeaders.Get("Authorization") != "Bearer sk-openai-test" {
		t.Errorf("auth header: got %q", seenHeaders.Get("Authorization"))
	}

	// Body: messages array (not split like Anthropic)
	msgs, _ := seenBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "You are helpful." {
		t.Errorf("system message not preserved: %v", first)
	}
}

func TestOpenAI_APIKeyHeaderAuth(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY","azure-key")
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Write([]byte(`{"id":"x","model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	_, err := NewOpenAICompatAdapter().Complete(context.Background(), types.LLMRequest{
		Model:    "m",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, openaiProvider(srv.URL, types.AuthAPIKeyHeader))
	if err != nil {
		t.Fatal(err)
	}
	if seen.Get("api-key") != "azure-key" {
		t.Errorf("api-key header: got %q", seen.Get("api-key"))
	}
	if seen.Get("Authorization") != "" {
		t.Errorf("Authorization header should NOT be set for AuthAPIKeyHeader")
	}
}

func TestOpenAI_NoAuth(t *testing.T) {
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Write([]byte(`{"id":"x","model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	// Ollama-style — no API key at all
	provider := types.LLMProvider{
		Name:       "ollama-local",
		BaseURL:    srv.URL,
		AuthMethod: types.AuthNone,
	}
	_, err := NewOpenAICompatAdapter().Complete(context.Background(), types.LLMRequest{
		Model:    "llama3",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, provider)
	if err != nil {
		t.Fatal(err)
	}
	if seen.Get("Authorization") != "" || seen.Get("api-key") != "" {
		t.Errorf("no auth headers expected, saw %v", seen)
	}
}

func TestOpenAI_BaseURLWithV1IsNotDoubled(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY","k")
	var seenPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.Write([]byte(`{"id":"x","model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	// Common real-world pattern: base_url already ends in /v1.
	provider := openaiProvider(srv.URL+"/v1", types.AuthBearer)
	_, err := NewOpenAICompatAdapter().Complete(context.Background(), types.LLMRequest{
		Model:    "m",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, provider)
	if err != nil {
		t.Fatal(err)
	}
	// Should be /v1/chat/completions, NOT /v1/v1/chat/completions
	if strings.Contains(seenPath, "/v1/v1/") {
		t.Errorf("v1 doubled in path: %q", seenPath)
	}
	if !strings.HasSuffix(seenPath, "/v1/chat/completions") {
		t.Errorf("unexpected path: %q", seenPath)
	}
}

func TestOpenAI_TrailingSlashInBaseURL(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY","k")
	var seenPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.Write([]byte(`{"id":"x","model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	provider := openaiProvider(srv.URL+"/", types.AuthBearer)
	_, err := NewOpenAICompatAdapter().Complete(context.Background(), types.LLMRequest{
		Model:    "m",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, provider)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(seenPath, "//") {
		t.Errorf("double slash in path: %q", seenPath)
	}
}

func TestOpenAI_FinishReasonLength(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY","k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"x","model":"m","choices":[{"message":{"content":"..."},"finish_reason":"length"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	resp, err := NewOpenAICompatAdapter().Complete(context.Background(), types.LLMRequest{
		Model:    "m",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, openaiProvider(srv.URL, types.AuthBearer))
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != "length" {
		t.Errorf("finish_reason=length should be preserved, got %q", resp.FinishReason)
	}
}

func TestOpenAI_500Error(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY","k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	_, err := NewOpenAICompatAdapter().Complete(context.Background(), types.LLMRequest{
		Model:    "m",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, openaiProvider(srv.URL, types.AuthBearer))
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should include status: %v", err)
	}
}

func TestOpenAI_EmptyChoicesIsError(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY","k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"x","model":"m","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0}}`))
	}))
	defer srv.Close()

	_, err := NewOpenAICompatAdapter().Complete(context.Background(), types.LLMRequest{
		Model:    "m",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, openaiProvider(srv.URL, types.AuthBearer))
	if err == nil {
		t.Fatal("expected error when choices array is empty")
	}
}

func TestOpenAI_CustomHeaders(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY","k")
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Write([]byte(`{"id":"x","model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	prov := openaiProvider(srv.URL, types.AuthBearer)
	prov.Config.CustomHeaders = map[string]string{"OpenAI-Organization": "org-abc"}

	_, err := NewOpenAICompatAdapter().Complete(context.Background(), types.LLMRequest{
		Model:    "m",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, prov)
	if err != nil {
		t.Fatal(err)
	}
	if seen.Get("OpenAI-Organization") != "org-abc" {
		t.Errorf("custom header missing")
	}
}

func TestOpenAI_RequestBodyShape(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY","k")
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Write([]byte(`{"id":"x","model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	_, err := NewOpenAICompatAdapter().Complete(context.Background(), types.LLMRequest{
		Model:       "gpt-4.1-mini",
		Messages:    []types.LLMMessage{{Role: "user", Content: "hi"}},
		Temperature: 0.5,
		MaxTokens:   128,
		Stop:        []string{"###"},
	}, openaiProvider(srv.URL, types.AuthBearer))
	if err != nil {
		t.Fatal(err)
	}

	if body["model"] != "gpt-4.1-mini" {
		t.Errorf("model in body: %v", body["model"])
	}
	if body["max_tokens"] != float64(128) {
		t.Errorf("max_tokens in body: %v", body["max_tokens"])
	}
	if body["temperature"] != 0.5 {
		t.Errorf("temperature in body: %v", body["temperature"])
	}
	stops, _ := body["stop"].([]any)
	if len(stops) != 1 || stops[0] != "###" {
		t.Errorf("stop sequences not forwarded: %v", stops)
	}
}
