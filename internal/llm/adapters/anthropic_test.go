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

func anthropicProvider(baseURL string) types.LLMProvider {
	key := "env://TEST_ANTHROPIC_KEY"
	return types.LLMProvider{
		Name:         "anthropic",
		BaseURL:      baseURL,
		AuthMethod:   types.AuthAPIKeyHeader,
		APIKeyRef:    &key,
		DefaultModel: "claude-sonnet-4-20250514",
	}
}

func TestAnthropic_HappyPath(t *testing.T) {
	t.Setenv("TEST_ANTHROPIC_KEY", "sk-ant-test-123")

	var seenHeaders http.Header
	var seenBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("expected path /messages, got %q", r.URL.Path)
		}
		seenHeaders = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &seenBody)

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "msg_01",
			"model": "claude-sonnet-4-20250514",
			"content": [{"type":"text","text":"Looks good."}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 42, "output_tokens": 7}
		}`))
	}))
	defer srv.Close()

	a := NewAnthropicAdapter()
	resp, err := a.Complete(context.Background(), types.LLMRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []types.LLMMessage{
			{Role: "system", Content: "You are a reviewer."},
			{Role: "user", Content: "Review this."},
		},
		Temperature: 0,
		MaxTokens:   1024,
	}, anthropicProvider(srv.URL))

	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Content != "Looks good." {
		t.Errorf("content: got %q", resp.Content)
	}
	if resp.InputTokens != 42 || resp.OutputTokens != 7 {
		t.Errorf("token counts wrong: in=%d out=%d", resp.InputTokens, resp.OutputTokens)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("finish_reason: got %q, want stop", resp.FinishReason)
	}
	if resp.Model != "claude-sonnet-4-20250514" {
		t.Errorf("model not echoed back")
	}

	// Auth + version headers
	if seenHeaders.Get("x-api-key") != "sk-ant-test-123" {
		t.Errorf("x-api-key: got %q", seenHeaders.Get("x-api-key"))
	}
	if seenHeaders.Get("anthropic-version") == "" {
		t.Errorf("anthropic-version header missing")
	}
	if seenHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type missing")
	}

	// System message extracted out of messages
	if seenBody["system"] != "You are a reviewer." {
		t.Errorf("system field wrong: %v", seenBody["system"])
	}
	msgs, _ := seenBody["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 non-system message, got %d", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "user" || first["content"] != "Review this." {
		t.Errorf("user message wrong: %v", first)
	}
}

func TestAnthropic_MaxTokensFinishReason(t *testing.T) {
	t.Setenv("TEST_ANTHROPIC_KEY", "k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"id":"msg_01","model":"m",
			"content":[{"type":"text","text":"..."}],
			"stop_reason":"max_tokens",
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	}))
	defer srv.Close()

	resp, err := NewAnthropicAdapter().Complete(context.Background(), types.LLMRequest{
		Model:    "m",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, anthropicProvider(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != "length" {
		t.Errorf("max_tokens stop_reason should map to 'length', got %q", resp.FinishReason)
	}
}

func TestAnthropic_NonOKStatusReturnsError(t *testing.T) {
	t.Setenv("TEST_ANTHROPIC_KEY", "k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	_, err := NewAnthropicAdapter().Complete(context.Background(), types.LLMRequest{
		Model:    "m",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, anthropicProvider(srv.URL))
	if err == nil {
		t.Fatal("expected error on 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should include status: %v", err)
	}
}

func TestAnthropic_MultipleTextBlocksAreConcatenated(t *testing.T) {
	t.Setenv("TEST_ANTHROPIC_KEY", "k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"id":"m","model":"m",
			"content":[
				{"type":"text","text":"Part A. "},
				{"type":"text","text":"Part B."}
			],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	}))
	defer srv.Close()

	resp, err := NewAnthropicAdapter().Complete(context.Background(), types.LLMRequest{
		Model:    "m",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, anthropicProvider(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Part A. Part B." {
		t.Errorf("should concatenate text blocks: %q", resp.Content)
	}
}

func TestAnthropic_NonTextBlocksIgnored(t *testing.T) {
	t.Setenv("TEST_ANTHROPIC_KEY", "k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"id":"m","model":"m",
			"content":[
				{"type":"tool_use","id":"t1","name":"foo","input":{}},
				{"type":"text","text":"visible"}
			],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	}))
	defer srv.Close()

	resp, err := NewAnthropicAdapter().Complete(context.Background(), types.LLMRequest{
		Model:    "m",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, anthropicProvider(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "visible" {
		t.Errorf("non-text blocks should be ignored, got %q", resp.Content)
	}
}

func TestAnthropic_CustomHeaders(t *testing.T) {
	t.Setenv("TEST_ANTHROPIC_KEY", "k")
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Write([]byte(`{"id":"m","model":"m","content":[{"type":"text","text":"x"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	prov := anthropicProvider(srv.URL)
	prov.Config.CustomHeaders = map[string]string{
		"anthropic-beta":  "prompt-caching-2024-07-31",
		"X-Request-Trace": "abc123",
	}

	_, err := NewAnthropicAdapter().Complete(context.Background(), types.LLMRequest{
		Model:    "m",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, prov)
	if err != nil {
		t.Fatal(err)
	}
	if seen.Get("anthropic-beta") != "prompt-caching-2024-07-31" {
		t.Errorf("custom header not set")
	}
	if seen.Get("X-Request-Trace") != "abc123" {
		t.Errorf("custom trace header not set")
	}
}

func TestAnthropic_SecretResolutionFailure(t *testing.T) {
	// Don't set TEST_ANTHROPIC_KEY — resolver should fail
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should never be called when secret resolution fails")
	}))
	defer srv.Close()

	// Clear any stale cache from previous tests
	t.Setenv("TEST_ANTHROPIC_KEY_MISSING", "")
	provider := types.LLMProvider{
		Name:       "anthropic",
		BaseURL:    srv.URL,
		AuthMethod: types.AuthAPIKeyHeader,
		APIKeyRef:  ptrString("env://TEST_ANTHROPIC_KEY_DEFINITELY_NOT_SET"),
	}
	_, err := NewAnthropicAdapter().Complete(context.Background(), types.LLMRequest{
		Model:    "m",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, provider)
	if err == nil {
		t.Fatal("expected error when API key ref cannot be resolved")
	}
}

func ptrString(s string) *string { return &s }
