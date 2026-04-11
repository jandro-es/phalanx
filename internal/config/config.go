// Package config loads Phalanx configuration from file, environment, and defaults.
package config

import (
	"os"
	"strconv"
)

// Config is the top-level Phalanx configuration.
type Config struct {
	Port        int
	Host        string
	DatabaseURL string
	RedisURL    string
	LogLevel    string

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

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
