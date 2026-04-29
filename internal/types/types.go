// Package types defines all domain types for Phalanx.
package types

import (
	"encoding/json"
	"time"
)

// =============================================================================
// Enums
// =============================================================================

type Verdict string

const (
	VerdictPass          Verdict = "pass"
	VerdictWarn          Verdict = "warn"
	VerdictFail          Verdict = "fail"
	VerdictError         Verdict = "error"
	VerdictNotApplicable Verdict = "not_applicable"
)

type Platform string

const (
	PlatformGitHub    Platform = "github"
	PlatformGitLab    Platform = "gitlab"
	PlatformBitbucket Platform = "bitbucket"
)

type ReviewStatus string

const (
	StatusPending   ReviewStatus = "pending"
	StatusQueued    ReviewStatus = "queued"
	StatusRunning   ReviewStatus = "running"
	StatusCompleted ReviewStatus = "completed"
	StatusFailed    ReviewStatus = "failed"
	StatusCancelled ReviewStatus = "cancelled"
)

type TriggerSource string

const (
	TriggerWebhook   TriggerSource = "webhook"
	TriggerCIAction  TriggerSource = "ci-action"
	TriggerCLI       TriggerSource = "cli"
	TriggerAPI       TriggerSource = "api"
	TriggerDashboard TriggerSource = "dashboard"
)

type DecisionType string

const (
	DecisionApprove        DecisionType = "approve"
	DecisionRequestChanges DecisionType = "request_changes"
	DecisionDefer          DecisionType = "defer"
)

type Severity string

const (
	SeverityCritical   Severity = "critical"
	SeverityMajor      Severity = "major"
	SeverityMinor      Severity = "minor"
	SeveritySuggestion Severity = "suggestion"
	SeverityInfo       Severity = "info"
)

type AuthMethod string

const (
	AuthBearer       AuthMethod = "bearer"
	AuthAPIKeyHeader AuthMethod = "api-key-header"
	AuthNone         AuthMethod = "none"
)

type AuditEventType string

const (
	AuditSessionCreated   AuditEventType = "session.created"
	AuditSessionQueued    AuditEventType = "session.queued"
	AuditSessionRunning   AuditEventType = "session.running"
	AuditSessionCompleted AuditEventType = "session.completed"
	AuditSessionFailed    AuditEventType = "session.failed"
	AuditAgentStarted     AuditEventType = "agent.started"
	AuditAgentCompleted   AuditEventType = "agent.completed"
	AuditAgentFailed      AuditEventType = "agent.failed"
	AuditAgentSkipped     AuditEventType = "agent.skipped"
	AuditLLMRequest       AuditEventType = "llm.request"
	AuditLLMResponse      AuditEventType = "llm.response"
	AuditLLMError         AuditEventType = "llm.error"
	AuditLLMFallback      AuditEventType = "llm.fallback"
	AuditReportPosted     AuditEventType = "report.posted"
	AuditReportFailed     AuditEventType = "report.failed"
	AuditDecisionApprove  AuditEventType = "decision.approve"
	AuditDecisionChanges  AuditEventType = "decision.request_changes"
	AuditDecisionDefer    AuditEventType = "decision.defer"
	AuditConfigCreated    AuditEventType = "config.agent.created"
	AuditConfigUpdated    AuditEventType = "config.agent.updated"
	AuditSkillCreated     AuditEventType = "config.skill.created"
	AuditSkillUpdated     AuditEventType = "config.skill.updated"
	AuditProviderCreated  AuditEventType = "config.provider.created"
	AuditProviderUpdated  AuditEventType = "config.provider.updated"
)

// =============================================================================
// LLM Provider
// =============================================================================

type LLMProvider struct {
	ID           string         `json:"id" db:"id"`
	Name         string         `json:"name" db:"name"`
	BaseURL      string         `json:"base_url" db:"base_url"`
	AuthMethod   AuthMethod     `json:"auth_method" db:"auth_method"`
	APIKeyRef    *string        `json:"api_key_ref" db:"api_key_ref"`
	DefaultModel string         `json:"default_model" db:"default_model"`
	Models       []string       `json:"models" db:"models"`
	Config       ProviderConfig `json:"config" db:"config"`
	CreatedAt    time.Time      `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at" db:"updated_at"`
}

type ProviderConfig struct {
	RequestsPerMinute int               `json:"requestsPerMinute,omitempty" yaml:"requests_per_minute"`
	TokensPerMinute   int               `json:"tokensPerMinute,omitempty" yaml:"tokens_per_minute"`
	TimeoutMs         int               `json:"timeoutMs,omitempty" yaml:"timeout_ms"`
	MaxRetries        int               `json:"maxRetries,omitempty" yaml:"max_retries"`
	RetryDelayMs      int               `json:"retryDelayMs,omitempty" yaml:"retry_delay_ms"`
	CustomHeaders     map[string]string `json:"customHeaders,omitempty" yaml:"custom_headers"`
}

// =============================================================================
// Skill
// =============================================================================

type Skill struct {
	ID                string          `json:"id" db:"id"`
	Slug              string          `json:"slug" db:"slug"`
	Name              string          `json:"name" db:"name"`
	Version           int             `json:"version" db:"version"`
	SystemPrompt      string          `json:"system_prompt" db:"system_prompt"`
	ChecklistTemplate string          `json:"checklist_template" db:"checklist_template"`
	OutputSchema      json.RawMessage `json:"output_schema,omitempty" db:"output_schema"`
	IsBuiltin         bool            `json:"is_builtin" db:"is_builtin"`
	Tags              []string        `json:"tags" db:"tags"`
	CreatedAt         time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at" db:"updated_at"`
}

// =============================================================================
// Context Document
// =============================================================================

type ContextDocument struct {
	ID        string    `json:"id" db:"id"`
	Name      string    `json:"name" db:"name"`
	Content   string    `json:"content" db:"content"`
	DocType   string    `json:"doc_type" db:"doc_type"`
	Tags      []string  `json:"tags" db:"tags"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// =============================================================================
// Agent
// =============================================================================

type Agent struct {
	ID            string      `json:"id" db:"id"`
	Name          string      `json:"name" db:"name"`
	SkillID       string      `json:"skill_id" db:"skill_id"`
	ProviderID    string      `json:"provider_id" db:"provider_id"`
	ModelOverride *string     `json:"model_override" db:"model_override"`
	Temperature   float64     `json:"temperature" db:"temperature"`
	MaxTokens     int         `json:"max_tokens" db:"max_tokens"`
	Enabled       bool        `json:"enabled" db:"enabled"`
	Priority      int         `json:"priority" db:"priority"`
	Config        AgentConfig `json:"config" db:"config"`
	CreatedAt     time.Time   `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at" db:"updated_at"`
}

type AgentConfig struct {
	MaxDiffTokens      int      `json:"maxDiffTokens,omitempty"`
	FilePatterns       []string `json:"filePatterns,omitempty"`
	IgnorePatterns     []string `json:"ignorePatterns,omitempty"`
	SkipIfNoMatch      bool     `json:"skipIfNoMatch,omitempty"`
	FallbackProviderID *string  `json:"fallbackProviderId,omitempty"`
	FallbackModel      *string  `json:"fallbackModel,omitempty"`
}

type AgentWithRelations struct {
	Agent
	Skill    Skill             `json:"skill"`
	Provider LLMProvider       `json:"provider"`
	Contexts []ContextDocument `json:"contexts"`
}

// =============================================================================
// Review Session
// =============================================================================

type ReviewSession struct {
	ID                 string          `json:"id" db:"id"`
	ExternalPRID       string          `json:"external_pr_id" db:"external_pr_id"`
	Platform           Platform        `json:"platform" db:"platform"`
	RepositoryFullName string          `json:"repository_full_name" db:"repository_full_name"`
	PRNumber           int             `json:"pr_number" db:"pr_number"`
	PRTitle            *string         `json:"pr_title" db:"pr_title"`
	PRAuthor           *string         `json:"pr_author" db:"pr_author"`
	PRURL              *string         `json:"pr_url" db:"pr_url"`
	HeadSHA            string          `json:"head_sha" db:"head_sha"`
	BaseSHA            string          `json:"base_sha" db:"base_sha"`
	BaseBranch         *string         `json:"base_branch" db:"base_branch"`
	HeadBranch         *string         `json:"head_branch" db:"head_branch"`
	DiffSnapshot       *string         `json:"diff_snapshot,omitempty" db:"diff_snapshot"`
	FileTree           json.RawMessage `json:"file_tree,omitempty" db:"file_tree"`
	Status             ReviewStatus    `json:"status" db:"status"`
	CompositeReport    *string         `json:"composite_report,omitempty" db:"composite_report"`
	OverallVerdict     *Verdict        `json:"overall_verdict" db:"overall_verdict"`
	TriggerSource      TriggerSource   `json:"trigger_source" db:"trigger_source"`
	Metadata           json.RawMessage `json:"metadata,omitempty" db:"metadata"`
	StartedAt          time.Time       `json:"started_at" db:"started_at"`
	CompletedAt        *time.Time      `json:"completed_at" db:"completed_at"`
}

type FileEntry struct {
	Path      string `json:"path"`
	Status    string `json:"status"` // added, modified, deleted, renamed
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	OldPath   string `json:"oldPath,omitempty"`
}

// =============================================================================
// Agent Report
// =============================================================================

type AgentReport struct {
	ID              string          `json:"id" db:"id"`
	SessionID       string          `json:"session_id" db:"session_id"`
	AgentID         string          `json:"agent_id" db:"agent_id"`
	SkillSlug       string          `json:"skill_slug" db:"skill_slug"`
	SkillVersion    int             `json:"skill_version" db:"skill_version"`
	ModelUsed       string          `json:"model_used" db:"model_used"`
	ProviderName    string          `json:"provider_name" db:"provider_name"`
	PromptHash      string          `json:"prompt_hash" db:"prompt_hash"`
	InputTokens     int             `json:"input_tokens" db:"input_tokens"`
	OutputTokens    int             `json:"output_tokens" db:"output_tokens"`
	LatencyMs       int             `json:"latency_ms" db:"latency_ms"`
	CostEstimateUSD *float64        `json:"cost_estimate_usd" db:"cost_estimate_usd"`
	RawResponse     string          `json:"raw_response" db:"raw_response"`
	ReportMD        string          `json:"report_md" db:"report_md"`
	ChecklistJSON   json.RawMessage `json:"checklist_json" db:"checklist_json"`
	Findings        json.RawMessage `json:"findings" db:"findings"`
	Verdict         Verdict         `json:"verdict" db:"verdict"`
	CreatedAt       time.Time       `json:"created_at" db:"created_at"`
}

type ChecklistItem struct {
	Item    string `json:"item"`
	Status  string `json:"status"` // pass, fail, na, warn
	Details string `json:"details,omitempty"`
}

type Finding struct {
	Severity  Severity `json:"severity"`
	File      string   `json:"file"`
	Lines     string   `json:"lines,omitempty"`
	Issue     string   `json:"issue"`
	Fix       string   `json:"fix"`
	Reference string   `json:"reference,omitempty"`
}

// =============================================================================
// Approval Decision
// =============================================================================

type ApprovalDecision struct {
	ID                 string          `json:"id" db:"id"`
	SessionID          string          `json:"session_id" db:"session_id"`
	Decision           DecisionType    `json:"decision" db:"decision"`
	EngineerID         string          `json:"engineer_id" db:"engineer_id"`
	EngineerName       string          `json:"engineer_name" db:"engineer_name"`
	EngineerEmail      *string         `json:"engineer_email" db:"engineer_email"`
	Justification      *string         `json:"justification" db:"justification"`
	OverriddenVerdicts json.RawMessage `json:"overridden_verdicts" db:"overridden_verdicts"`
	DecidedAt          time.Time       `json:"decided_at" db:"decided_at"`
}

// VerdictOverride is sent in POST /api/decisions request bodies so it uses
// camelCase to match the client convention for request payloads.
type VerdictOverride struct {
	AgentReportID   string  `json:"agentReportId"`
	SkillSlug       string  `json:"skillSlug"`
	OriginalVerdict Verdict `json:"originalVerdict"`
	OverriddenTo    Verdict `json:"overriddenTo"`
	Reason          string  `json:"reason"`
}

// =============================================================================
// Audit
// =============================================================================

type AuditEntry struct {
	ID          int64           `json:"id" db:"id"`
	EventType   AuditEventType  `json:"event_type" db:"event_type"`
	SessionID   *string         `json:"session_id" db:"session_id"`
	AgentID     *string         `json:"agent_id" db:"agent_id"`
	Actor       string          `json:"actor" db:"actor"`
	Payload     json.RawMessage `json:"payload" db:"payload"`
	PayloadHash *string         `json:"payload_hash,omitempty" db:"payload_hash"`
	PrevHash    *string         `json:"prev_hash,omitempty" db:"prev_hash"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
}

// =============================================================================
// LLM Message types
// =============================================================================

type LLMMessage struct {
	Role    string `json:"role"` // system, user, assistant
	Content string `json:"content"`
}

type LLMRequest struct {
	Provider    string       `json:"provider"`
	Model       string       `json:"model"`
	Messages    []LLMMessage `json:"messages"`
	Temperature float64      `json:"temperature"`
	MaxTokens   int          `json:"maxTokens"`
	Stop        []string     `json:"stop,omitempty"`
}

type LLMResponse struct {
	Content      string `json:"content"`
	Model        string `json:"model"`
	InputTokens  int    `json:"inputTokens"`
	OutputTokens int    `json:"outputTokens"`
	LatencyMs    int    `json:"latencyMs"`
	Provider     string `json:"provider"`
	FinishReason string `json:"finishReason"` // stop, length, error
}

// =============================================================================
// Composite Report
// =============================================================================

type CompositeReport struct {
	SessionID      string         `json:"session_id"`
	Markdown       string         `json:"markdown"`
	OverallVerdict Verdict        `json:"overall_verdict"`
	AgentSummaries []AgentSummary `json:"agent_summaries"`
	GeneratedAt    time.Time      `json:"generated_at"`
}

type AgentSummary struct {
	SkillSlug     string  `json:"skill_slug"`
	SkillName     string  `json:"skill_name"`
	Verdict       Verdict `json:"verdict"`
	FindingsCount int     `json:"findings_count"`
	CriticalCount int     `json:"critical_count"`
	MajorCount    int     `json:"major_count"`
	KeyFinding    *string `json:"key_finding"`
	LatencyMs     int     `json:"latency_ms"`
}

// =============================================================================
// API Request/Response
// =============================================================================

type TriggerReviewRequest struct {
	Platform      Platform      `json:"platform" validate:"required"`
	Repository    string        `json:"repository" validate:"required"`
	PRNumber      int           `json:"prNumber" validate:"required,gt=0"`
	HeadSHA       string        `json:"headSha" validate:"required,min=7"`
	BaseSHA       string        `json:"baseSha" validate:"required,min=7"`
	Diff          *string       `json:"diff,omitempty"`
	Agents        []string      `json:"agents,omitempty"`
	TriggerSource TriggerSource `json:"triggerSource" validate:"required"`
	CallbackURL   *string       `json:"callbackUrl,omitempty"`
}

type SubmitDecisionRequest struct {
	Decision           DecisionType    `json:"decision" validate:"required"`
	EngineerID         string          `json:"engineerId" validate:"required"`
	EngineerName       string          `json:"engineerName" validate:"required"`
	EngineerEmail      *string         `json:"engineerEmail,omitempty"`
	Justification      *string         `json:"justification,omitempty"`
	OverriddenVerdicts []VerdictOverride `json:"overriddenVerdicts"`
}

type CreateAgentRequest struct {
	Name          string      `json:"name" validate:"required"`
	SkillID       string      `json:"skillId" validate:"required,uuid"`
	ProviderID    string      `json:"providerId" validate:"required,uuid"`
	ModelOverride *string     `json:"modelOverride,omitempty"`
	Temperature   float64     `json:"temperature"`
	MaxTokens     int         `json:"maxTokens"`
	Enabled       bool        `json:"enabled"`
	Priority      int         `json:"priority"`
	Config        AgentConfig `json:"config"`
	ContextIDs    []string    `json:"contextIds"`
}

type CreateSkillRequest struct {
	Slug              string          `json:"slug" validate:"required"`
	Name              string          `json:"name" validate:"required"`
	Version           int             `json:"version"`
	SystemPrompt      string          `json:"systemPrompt" validate:"required"`
	ChecklistTemplate string          `json:"checklistTemplate" validate:"required"`
	OutputSchema      json.RawMessage `json:"outputSchema,omitempty"`
	Tags              []string        `json:"tags"`
}

type CreateProviderRequest struct {
	Name         string         `json:"name" validate:"required"`
	BaseURL      string         `json:"baseUrl" validate:"required,url"`
	AuthMethod   AuthMethod     `json:"authMethod" validate:"required"`
	APIKeyRef    *string        `json:"apiKeyRef,omitempty"`
	DefaultModel string         `json:"defaultModel" validate:"required"`
	Models       []string       `json:"models"`
	Config       ProviderConfig `json:"config"`
}

// CreateContextRequest mirrors ContextDocument minus server-managed columns.
// DocType must be one of `guideline`, `non-negotiable`, `reference`, `example`
// per the migration's CHECK constraint.
type CreateContextRequest struct {
	Name    string   `json:"name" validate:"required"`
	Content string   `json:"content" validate:"required"`
	DocType string   `json:"docType" validate:"required,oneof=guideline non-negotiable reference example"`
	Tags    []string `json:"tags"`
}
