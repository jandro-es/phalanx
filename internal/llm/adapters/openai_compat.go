package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/phalanx-ai/phalanx/internal/secrets"
	"github.com/phalanx-ai/phalanx/internal/types"
)

// OpenAICompatAdapter covers OpenAI, DeepSeek, vLLM, Ollama, LiteLLM, Groq, etc.
type OpenAICompatAdapter struct {
	client *http.Client
}

func NewOpenAICompatAdapter() *OpenAICompatAdapter {
	return &OpenAICompatAdapter{
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Stop        []string        `json:"stop,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (a *OpenAICompatAdapter) Complete(ctx context.Context, req types.LLMRequest, provider types.LLMProvider) (*types.LLMResponse, error) {
	apiKey := ""
	if provider.APIKeyRef != nil {
		var err error
		apiKey, err = secrets.Resolve(*provider.APIKeyRef)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve API key: %w", err)
		}
	}

	var msgs []openAIMessage
	for _, m := range req.Messages {
		msgs = append(msgs, openAIMessage{Role: m.Role, Content: m.Content})
	}

	temp := req.Temperature
	body := openAIRequest{
		Model:       req.Model,
		Messages:    msgs,
		Temperature: &temp,
		MaxTokens:   req.MaxTokens,
		Stop:        req.Stop,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	// Normalise URL
	baseURL := strings.TrimRight(provider.BaseURL, "/")
	endpoint := baseURL + "/chat/completions"
	if !strings.HasSuffix(baseURL, "/v1") {
		endpoint = baseURL + "/v1/chat/completions"
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	switch provider.AuthMethod {
	case types.AuthBearer:
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	case types.AuthAPIKeyHeader:
		httpReq.Header.Set("api-key", apiKey)
	}
	for k, v := range provider.Config.CustomHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", provider.Name, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s API %d: %s", provider.Name, resp.StatusCode, string(respBody))
	}

	var result openAIResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse %s response: %w", provider.Name, err)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("%s returned no choices", provider.Name)
	}

	finishReason := "stop"
	if result.Choices[0].FinishReason == "length" {
		finishReason = "length"
	}

	return &types.LLMResponse{
		Content:      result.Choices[0].Message.Content,
		Model:        result.Model,
		InputTokens:  result.Usage.PromptTokens,
		OutputTokens: result.Usage.CompletionTokens,
		Provider:     provider.Name,
		FinishReason: finishReason,
	}, nil
}
