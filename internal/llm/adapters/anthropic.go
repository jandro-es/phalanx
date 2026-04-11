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

// AnthropicAdapter maps to Anthropic's /v1/messages API.
type AnthropicAdapter struct {
	client *http.Client
}

func NewAnthropicAdapter() *AnthropicAdapter {
	return &AnthropicAdapter{
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Stop        []string           `json:"stop_sequences,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (a *AnthropicAdapter) Complete(ctx context.Context, req types.LLMRequest, provider types.LLMProvider) (*types.LLMResponse, error) {
	apiKey := ""
	if provider.APIKeyRef != nil {
		var err error
		apiKey, err = secrets.Resolve(*provider.APIKeyRef)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve API key: %w", err)
		}
	}

	// Separate system message
	var system string
	var msgs []anthropicMessage
	for _, m := range req.Messages {
		if m.Role == "system" {
			system = m.Content
		} else {
			msgs = append(msgs, anthropicMessage{Role: m.Role, Content: m.Content})
		}
	}

	temp := req.Temperature
	body := anthropicRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: &temp,
		System:      system,
		Messages:    msgs,
		Stop:        req.Stop,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(provider.BaseURL, "/")+"/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	for k, v := range provider.Config.CustomHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("anthropic API %d: %s", resp.StatusCode, string(respBody))
	}

	var result anthropicResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse anthropic response: %w", err)
	}

	var content strings.Builder
	for _, block := range result.Content {
		if block.Type == "text" {
			content.WriteString(block.Text)
		}
	}

	finishReason := "stop"
	if result.StopReason == "max_tokens" {
		finishReason = "length"
	}

	return &types.LLMResponse{
		Content:      content.String(),
		Model:        result.Model,
		InputTokens:  result.Usage.InputTokens,
		OutputTokens: result.Usage.OutputTokens,
		Provider:     provider.Name,
		FinishReason: finishReason,
	}, nil
}
