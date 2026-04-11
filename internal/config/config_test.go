package config

import (
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear every env var the loader might see.
	for _, k := range []string{
		"PORT", "HOST", "DATABASE_URL", "REDIS_URL", "LOG_LEVEL",
		"PHALANX_QUEUE_CONCURRENCY", "PHALANX_QUEUE_MAX_RETRIES", "PHALANX_QUEUE_JOB_TIMEOUT",
		"PHALANX_AUDIT_HASH_CHAIN", "PHALANX_AUDIT_RETENTION_DAYS",
		"PHALANX_MAX_DIFF_SIZE", "PHALANX_POST_COMMENT", "PHALANX_CREATE_CHECK_RUN", "PHALANX_BLOCK_MERGE_ON_FAIL",
		"GITHUB_TOKEN", "GITHUB_WEBHOOK_SECRET", "GITHUB_API_URL",
		"GITLAB_TOKEN", "GITLAB_WEBHOOK_SECRET", "GITLAB_URL",
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "DEEPSEEK_API_KEY",
	} {
		t.Setenv(k, "")
	}

	cfg := Load()

	if cfg.Port != 3100 {
		t.Errorf("Port default: got %d, want 3100", cfg.Port)
	}
	if cfg.Host != "0.0.0.0" {
		t.Errorf("Host default: got %q, want 0.0.0.0", cfg.Host)
	}
	if cfg.DatabaseURL != "postgresql://phalanx:phalanx@localhost:5432/phalanx" {
		t.Errorf("DatabaseURL default wrong: %q", cfg.DatabaseURL)
	}
	if cfg.RedisURL != "redis://localhost:6379" {
		t.Errorf("RedisURL default wrong: %q", cfg.RedisURL)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel default wrong: %q", cfg.LogLevel)
	}
	if cfg.QueueConcurrency != 10 {
		t.Errorf("QueueConcurrency default: got %d, want 10", cfg.QueueConcurrency)
	}
	if cfg.QueueMaxRetries != 2 {
		t.Errorf("QueueMaxRetries default: got %d, want 2", cfg.QueueMaxRetries)
	}
	if cfg.AuditHashChain {
		t.Errorf("AuditHashChain default should be false")
	}
	if !cfg.PostCommentOnPR {
		t.Errorf("PostCommentOnPR default should be true")
	}
	if !cfg.BlockMergeOnFail {
		t.Errorf("BlockMergeOnFail default should be true")
	}
	if cfg.GitHubAPIURL != "https://api.github.com" {
		t.Errorf("GitHubAPIURL default wrong: %q", cfg.GitHubAPIURL)
	}
	if cfg.GitLabURL != "https://gitlab.com" {
		t.Errorf("GitLabURL default wrong: %q", cfg.GitLabURL)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("PORT", "8080")
	t.Setenv("HOST", "127.0.0.1")
	t.Setenv("DATABASE_URL", "postgresql://u:p@db:5432/d")
	t.Setenv("REDIS_URL", "redis://cache:6379/3")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("PHALANX_QUEUE_CONCURRENCY", "25")
	t.Setenv("PHALANX_QUEUE_MAX_RETRIES", "5")
	t.Setenv("PHALANX_QUEUE_JOB_TIMEOUT", "60000")
	t.Setenv("PHALANX_AUDIT_HASH_CHAIN", "true")
	t.Setenv("PHALANX_POST_COMMENT", "false")
	t.Setenv("PHALANX_BLOCK_MERGE_ON_FAIL", "false")
	t.Setenv("GITHUB_TOKEN", "ghp_abc")
	t.Setenv("GITHUB_API_URL", "https://github.enterprise/api/v3")
	t.Setenv("GITLAB_URL", "https://gitlab.internal")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-xxx")

	cfg := Load()

	if cfg.Port != 8080 {
		t.Errorf("Port override: got %d, want 8080", cfg.Port)
	}
	if cfg.Host != "127.0.0.1" {
		t.Errorf("Host override wrong: %q", cfg.Host)
	}
	if cfg.DatabaseURL != "postgresql://u:p@db:5432/d" {
		t.Errorf("DatabaseURL override wrong: %q", cfg.DatabaseURL)
	}
	if cfg.RedisURL != "redis://cache:6379/3" {
		t.Errorf("RedisURL override wrong: %q", cfg.RedisURL)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel override wrong: %q", cfg.LogLevel)
	}
	if cfg.QueueConcurrency != 25 {
		t.Errorf("QueueConcurrency override: got %d, want 25", cfg.QueueConcurrency)
	}
	if cfg.QueueMaxRetries != 5 {
		t.Errorf("QueueMaxRetries override: got %d, want 5", cfg.QueueMaxRetries)
	}
	if !cfg.AuditHashChain {
		t.Errorf("AuditHashChain override should be true")
	}
	if cfg.PostCommentOnPR {
		t.Errorf("PostCommentOnPR override should be false")
	}
	if cfg.BlockMergeOnFail {
		t.Errorf("BlockMergeOnFail override should be false")
	}
	if cfg.GitHubToken != "ghp_abc" {
		t.Errorf("GitHubToken not picked up")
	}
	if cfg.GitHubAPIURL != "https://github.enterprise/api/v3" {
		t.Errorf("GitHubAPIURL override wrong")
	}
	if cfg.AnthropicAPIKey != "sk-ant-xxx" {
		t.Errorf("AnthropicAPIKey override wrong")
	}
}

func TestLoad_InvalidIntFallsBackToDefault(t *testing.T) {
	t.Setenv("PORT", "not-a-number")
	t.Setenv("PHALANX_QUEUE_CONCURRENCY", "")

	cfg := Load()

	if cfg.Port != 3100 {
		t.Errorf("invalid int should fall back to default, got %d", cfg.Port)
	}
}

func TestLoad_InvalidBoolFallsBackToDefault(t *testing.T) {
	t.Setenv("PHALANX_AUDIT_HASH_CHAIN", "maybe")
	t.Setenv("PHALANX_POST_COMMENT", "")

	cfg := Load()

	if cfg.AuditHashChain {
		t.Errorf("invalid bool should fall back to default (false)")
	}
	if !cfg.PostCommentOnPR {
		t.Errorf("empty bool should fall back to default (true)")
	}
}

func TestEnvStrHelpers(t *testing.T) {
	t.Setenv("PHALANX_TEST_STR", "hello")
	if got := envStr("PHALANX_TEST_STR", "default"); got != "hello" {
		t.Errorf("envStr: got %q, want hello", got)
	}
	if got := envStr("PHALANX_TEST_ABSENT", "default"); got != "default" {
		t.Errorf("envStr fallback: got %q, want default", got)
	}
}

func TestEnvIntHelpers(t *testing.T) {
	t.Setenv("PHALANX_TEST_INT", "42")
	if got := envInt("PHALANX_TEST_INT", 10); got != 42 {
		t.Errorf("envInt: got %d, want 42", got)
	}
	if got := envInt("PHALANX_TEST_ABSENT_INT", 10); got != 10 {
		t.Errorf("envInt fallback: got %d, want 10", got)
	}
	t.Setenv("PHALANX_TEST_BAD_INT", "nope")
	if got := envInt("PHALANX_TEST_BAD_INT", 10); got != 10 {
		t.Errorf("envInt on invalid: got %d, want 10", got)
	}
}

func TestEnvBoolHelpers(t *testing.T) {
	t.Setenv("PHALANX_TEST_BOOL_T", "true")
	t.Setenv("PHALANX_TEST_BOOL_F", "false")
	t.Setenv("PHALANX_TEST_BOOL_1", "1")
	t.Setenv("PHALANX_TEST_BOOL_0", "0")
	t.Setenv("PHALANX_TEST_BOOL_BAD", "yes")

	if !envBool("PHALANX_TEST_BOOL_T", false) {
		t.Errorf("envBool true wrong")
	}
	if envBool("PHALANX_TEST_BOOL_F", true) {
		t.Errorf("envBool false wrong")
	}
	if !envBool("PHALANX_TEST_BOOL_1", false) {
		t.Errorf("envBool 1 wrong")
	}
	if envBool("PHALANX_TEST_BOOL_0", true) {
		t.Errorf("envBool 0 wrong")
	}
	if !envBool("PHALANX_TEST_BOOL_BAD", true) {
		t.Errorf("envBool invalid should fall back to default")
	}
	if envBool("PHALANX_TEST_BOOL_ABSENT", false) {
		t.Errorf("envBool absent fallback wrong")
	}
}
