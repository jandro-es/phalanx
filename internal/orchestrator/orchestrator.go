// Package orchestrator coordinates full review sessions: loads agents,
// fans out execution in parallel, collects results, builds composite reports.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/phalanx-ai/phalanx/internal/agent"
	"github.com/phalanx-ai/phalanx/internal/audit"
	"github.com/phalanx-ai/phalanx/internal/llm"
	"github.com/phalanx-ai/phalanx/internal/platform"
	"github.com/phalanx-ai/phalanx/internal/report"
	"github.com/phalanx-ai/phalanx/internal/types"
)

// Orchestrator manages the review lifecycle.
type Orchestrator struct {
	db        *pgxpool.Pool
	audit     *audit.Logger
	router    *llm.Router
	builder   *report.Builder
	platforms map[types.Platform]platform.Client
	maxPar    int
}

// New creates a new orchestrator.
func New(
	db *pgxpool.Pool,
	auditLogger *audit.Logger,
	router *llm.Router,
	builder *report.Builder,
	platforms map[types.Platform]platform.Client,
	maxParallel int,
) *Orchestrator {
	if maxParallel <= 0 {
		maxParallel = 10
	}
	return &Orchestrator{
		db: db, audit: auditLogger, router: router,
		builder: builder, platforms: platforms, maxPar: maxParallel,
	}
}

// ExecuteReview runs a full review session.
func (o *Orchestrator) ExecuteReview(ctx context.Context, session types.ReviewSession) (*types.CompositeReport, error) {
	// Update status
	o.setStatus(ctx, session.ID, types.StatusRunning)
	o.audit.Log(ctx, audit.Event{
		EventType: types.AuditSessionRunning,
		SessionID: &session.ID,
		Actor:     "system",
		Payload:   map[string]any{},
	})

	// 1. Fetch diff if not stored
	diff := ""
	if session.DiffSnapshot != nil {
		diff = *session.DiffSnapshot
	}
	var fileTree []types.FileEntry
	if session.FileTree != nil {
		json.Unmarshal(session.FileTree, &fileTree)
	}

	if diff == "" {
		client, ok := o.platforms[session.Platform]
		if !ok {
			return nil, fmt.Errorf("no client for platform %s", session.Platform)
		}
		result, err := client.FetchDiff(ctx, session.RepositoryFullName, session.BaseSHA, session.HeadSHA)
		if err != nil {
			o.setStatus(ctx, session.ID, types.StatusFailed)
			return nil, fmt.Errorf("failed to fetch diff: %w", err)
		}
		diff = result.Diff
		fileTree = result.Files

		// Store for reproducibility
		ftJSON, _ := json.Marshal(fileTree)
		o.db.Exec(ctx,
			`UPDATE review_sessions SET diff_snapshot = $1, file_tree = $2 WHERE id = $3`,
			diff, ftJSON, session.ID)
	}

	// 2. Load enabled agents
	agents, err := o.loadAgents(ctx)
	if err != nil {
		o.setStatus(ctx, session.ID, types.StatusFailed)
		return nil, fmt.Errorf("failed to load agents: %w", err)
	}

	o.audit.Log(ctx, audit.Event{
		EventType: types.AuditSessionRunning,
		SessionID: &session.ID,
		Actor:     "system",
		Payload: map[string]any{
			"agentCount": len(agents),
			"agents":     agentSlugs(agents),
		},
	})

	// 3. Fan out agents with bounded parallelism
	runtime := agent.NewRuntime(o.router, o.audit)
	sem := make(chan struct{}, o.maxPar)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var reports []types.AgentReport

	for _, ag := range agents {
		wg.Add(1)
		go func(a types.AgentWithRelations) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result, err := runtime.Execute(ctx, agent.Input{
				Session:  session,
				Agent:    a,
				Diff:     diff,
				FileTree: fileTree,
			})

			if err != nil {
				// Create error report
				result = &agent.Result{
					Report: types.AgentReport{
						SessionID:    session.ID,
						AgentID:      a.ID,
						SkillSlug:    a.Skill.Slug,
						SkillVersion: a.Skill.Version,
						ModelUsed:    "error",
						ProviderName: a.Provider.Name,
						PromptHash:   "error",
						ReportMD:     fmt.Sprintf("## %s\n\n**Verdict:** ❌ ERROR\n\n*%s*", a.Skill.Name, err.Error()),
						Verdict:      types.VerdictError,
					},
				}
			}

			// Persist report
			o.db.Exec(ctx,
				`INSERT INTO agent_reports
				  (session_id, agent_id, skill_slug, skill_version, model_used, provider_name,
				   prompt_hash, input_tokens, output_tokens, latency_ms, cost_estimate_usd,
				   raw_response, report_md, checklist_json, findings, verdict)
				 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
				result.Report.SessionID, result.Report.AgentID,
				result.Report.SkillSlug, result.Report.SkillVersion,
				result.Report.ModelUsed, result.Report.ProviderName,
				result.Report.PromptHash, result.Report.InputTokens,
				result.Report.OutputTokens, result.Report.LatencyMs,
				result.Report.CostEstimateUSD, result.Report.RawResponse,
				result.Report.ReportMD, result.Report.ChecklistJSON,
				result.Report.Findings, result.Report.Verdict,
			)

			mu.Lock()
			reports = append(reports, result.Report)
			mu.Unlock()
		}(ag)
	}

	wg.Wait()

	// 4. Build composite report
	composite := o.builder.BuildComposite(session, reports)

	// 5. Update session
	v := composite.OverallVerdict
	now := time.Now()
	o.db.Exec(ctx,
		`UPDATE review_sessions SET status = 'completed', composite_report = $1,
		 overall_verdict = $2, completed_at = $3 WHERE id = $4`,
		composite.Markdown, v, now, session.ID)

	// 6. Post to git platform
	if client, ok := o.platforms[session.Platform]; ok {
		client.PostReview(ctx, session, *composite)
		o.audit.Log(ctx, audit.Event{
			EventType: types.AuditReportPosted,
			SessionID: &session.ID,
			Actor:     "system",
			Payload: map[string]any{
				"platform":       session.Platform,
				"overallVerdict": composite.OverallVerdict,
			},
		})
	}

	o.audit.Log(ctx, audit.Event{
		EventType: types.AuditSessionCompleted,
		SessionID: &session.ID,
		Actor:     "system",
		Payload: map[string]any{
			"overallVerdict": composite.OverallVerdict,
			"agentCount":    len(reports),
		},
	})

	return composite, nil
}

func (o *Orchestrator) setStatus(ctx context.Context, sessionID string, status types.ReviewStatus) {
	o.db.Exec(ctx, `UPDATE review_sessions SET status = $1 WHERE id = $2`, status, sessionID)
}

func (o *Orchestrator) loadAgents(ctx context.Context) ([]types.AgentWithRelations, error) {
	rows, err := o.db.Query(ctx, `
		SELECT a.id, a.name, a.skill_id, a.provider_id, a.model_override,
		       a.temperature, a.max_tokens, a.enabled, a.priority, a.config,
		       s.id, s.slug, s.name, s.version, s.system_prompt, s.checklist_template,
		       s.output_schema, s.is_builtin, s.tags,
		       p.id, p.name, p.base_url, p.auth_method, p.api_key_ref,
		       p.default_model, p.models, p.config
		FROM agents a
		JOIN skills s ON a.skill_id = s.id
		JOIN llm_providers p ON a.provider_id = p.id
		WHERE a.enabled = true
		ORDER BY a.priority ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []types.AgentWithRelations
	for rows.Next() {
		var a types.AgentWithRelations
		var agentConfig, providerConfig, outputSchema json.RawMessage
		err := rows.Scan(
			&a.ID, &a.Name, &a.SkillID, &a.ProviderID, &a.ModelOverride,
			&a.Temperature, &a.MaxTokens, &a.Enabled, &a.Priority, &agentConfig,
			&a.Skill.ID, &a.Skill.Slug, &a.Skill.Name, &a.Skill.Version,
			&a.Skill.SystemPrompt, &a.Skill.ChecklistTemplate,
			&outputSchema, &a.Skill.IsBuiltin, &a.Skill.Tags,
			&a.Provider.ID, &a.Provider.Name, &a.Provider.BaseURL,
			&a.Provider.AuthMethod, &a.Provider.APIKeyRef,
			&a.Provider.DefaultModel, &a.Provider.Models, &providerConfig,
		)
		if err != nil {
			return nil, err
		}
		json.Unmarshal(agentConfig, &a.Config)
		json.Unmarshal(providerConfig, &a.Provider.Config)
		a.Skill.OutputSchema = outputSchema

		// Load contexts
		ctxRows, err := o.db.Query(ctx,
			`SELECT cd.id, cd.name, cd.content, cd.doc_type, cd.tags
			 FROM context_documents cd JOIN agent_context ac ON cd.id = ac.context_id
			 WHERE ac.agent_id = $1`, a.ID)
		if err == nil {
			for ctxRows.Next() {
				var c types.ContextDocument
				ctxRows.Scan(&c.ID, &c.Name, &c.Content, &c.DocType, &c.Tags)
				a.Contexts = append(a.Contexts, c)
			}
			ctxRows.Close()
		}

		agents = append(agents, a)
	}
	return agents, nil
}

func agentSlugs(agents []types.AgentWithRelations) []string {
	slugs := make([]string, len(agents))
	for i, a := range agents {
		slugs[i] = a.Skill.Slug
	}
	return slugs
}
