// Package config loads Phalanx configuration from file, environment, and defaults.
package config

import (
	"os"
	"strconv"
	"strings"
)

// Config is the top-level Phalanx configuration.
type Config struct {
	Port        int
	Host        string
	DatabaseURL string
	RedisURL    string
	LogLevel    string

	// API auth
	APITokens          []string // PHALANX_API_TOKENS, comma-separated; empty = auth disabled
	CORSAllowedOrigins []string // PHALANX_CORS_ALLOWED_ORIGINS; empty = no CORS

	// Queue
	QueueConcurrency int
	QueueMaxRetries  int
	QueueJobTimeout  int // ms

	// Audit
	AuditHashChain    bool
	AuditRetentionDays int

	// Review
	MaxDiffSizeBytes int
	PostCommentOnPR  bool
	CreateCheckRun   bool
	BlockMergeOnFail bool

	// Git platforms
	GitHubToken         string
	GitHubWebhookSecret string
	GitHubAPIURL        string
	GitLabToken         string
	GitLabWebhookSecret string
	GitLabURL           string

	BitbucketAuth        string // "username:app_password" or "x-token-auth:<token>"
	BitbucketAPIURL      string // override for Bitbucket Server / DC; defaults to bitbucket.org
	BitbucketWebhookUUID string // X-Hook-UUID expected on inbound webhooks

	// LLM keys
	AnthropicAPIKey string
	OpenAIAPIKey    string
	DeepSeekAPIKey  string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		Port:        envInt("PORT", 3100),
		Host:        envStr("HOST", "0.0.0.0"),
		DatabaseURL: envStr("DATABASE_URL", "postgresql://phalanx:phalanx@localhost:5432/phalanx"),
		RedisURL:    envStr("REDIS_URL", "redis://localhost:6379"),
		LogLevel:    envStr("LOG_LEVEL", "info"),

		APITokens:          envCSV("PHALANX_API_TOKENS"),
		CORSAllowedOrigins: envCSV("PHALANX_CORS_ALLOWED_ORIGINS"),

		QueueConcurrency: envInt("PHALANX_QUEUE_CONCURRENCY", 10),
		QueueMaxRetries:  envInt("PHALANX_QUEUE_MAX_RETRIES", 2),
		QueueJobTimeout:  envInt("PHALANX_QUEUE_JOB_TIMEOUT", 120000),

		AuditHashChain:     envBool("PHALANX_AUDIT_HASH_CHAIN", false),
		AuditRetentionDays: envInt("PHALANX_AUDIT_RETENTION_DAYS", 1095),

		MaxDiffSizeBytes: envInt("PHALANX_MAX_DIFF_SIZE", 5000000),
		PostCommentOnPR:  envBool("PHALANX_POST_COMMENT", true),
		CreateCheckRun:   envBool("PHALANX_CREATE_CHECK_RUN", true),
		BlockMergeOnFail: envBool("PHALANX_BLOCK_MERGE_ON_FAIL", true),

		GitHubToken:         envStr("GITHUB_TOKEN", ""),
		GitHubWebhookSecret: envStr("GITHUB_WEBHOOK_SECRET", ""),
		GitHubAPIURL:        envStr("GITHUB_API_URL", "https://api.github.com"),
		GitLabToken:         envStr("GITLAB_TOKEN", ""),
		GitLabWebhookSecret: envStr("GITLAB_WEBHOOK_SECRET", ""),
		GitLabURL:           envStr("GITLAB_URL", "https://gitlab.com"),

		BitbucketAuth:        envStr("BITBUCKET_AUTH", ""),
		BitbucketAPIURL:      envStr("BITBUCKET_API_URL", "https://api.bitbucket.org/2.0"),
		BitbucketWebhookUUID: envStr("BITBUCKET_WEBHOOK_UUID", ""),

		AnthropicAPIKey: envStr("ANTHROPIC_API_KEY", ""),
		OpenAIAPIKey:    envStr("OPENAI_API_KEY", ""),
		DeepSeekAPIKey:  envStr("DEEPSEEK_API_KEY", ""),
	}
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

// envCSV reads a comma-separated env var into a trimmed, non-empty slice.
// Returns nil when unset so callers can distinguish "no value" from "[]".
func envCSV(key string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
