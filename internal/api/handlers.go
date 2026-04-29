// Package api provides HTTP handlers for the Phalanx API.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/phalanx-ai/phalanx/internal/audit"
	"github.com/phalanx-ai/phalanx/internal/types"
)

// ReviewEnqueuer hands a review session off to a worker (queue or in-process).
type ReviewEnqueuer interface {
	EnqueueReview(ctx context.Context, session types.ReviewSession) error
}

// Handler holds shared dependencies for all routes.
type Handler struct {
	DB       *pgxpool.Pool
	Audit    *audit.Logger
	Enqueuer ReviewEnqueuer

	// GitHubWebhookSecret / GitLabWebhookSecret / BitbucketWebhookUUID are
	// HMAC / shared secrets used to authenticate inbound webhook deliveries.
	// Empty disables verification.
	//
	// Bitbucket Cloud signs webhooks with a per-webhook UUID delivered as
	// `X-Hook-UUID`; we treat it as a shared secret like GitLab's token.
	GitHubWebhookSecret string
	GitLabWebhookSecret string
	BitbucketWebhookUUID string
}

// Routes returns a chi.Router with all API routes mounted.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Route("/api", func(r chi.Router) {
		// Webhooks
		r.Post("/webhooks/github", h.githubWebhook)
		r.Post("/webhooks/gitlab", h.gitlabWebhook)
		r.Post("/webhooks/bitbucket", h.bitbucketWebhook)

		// Reviews
		r.Post("/reviews", h.triggerReview)
		r.Get("/reviews/{sessionID}", h.getSession)
		r.Get("/reviews", h.listSessions)
		r.Post("/reviews/{sessionID}/rerun", h.rerunReview)

		// Decisions
		r.Post("/decisions/{sessionID}", h.submitDecision)
		r.Get("/decisions/{sessionID}", h.getDecisions)
		r.Get("/decisions/by-engineer/{engineerID}", h.getDecisionsByEngineer)

		// Agents
		r.Get("/agents", h.listAgents)
		r.Post("/agents", h.createAgent)
		r.Get("/agents/{id}", h.getAgent)
		r.Put("/agents/{id}", h.updateAgent)
		r.Delete("/agents/{id}", h.disableAgent)

		// Skills
		r.Get("/skills", h.listSkills)
		r.Post("/skills", h.createSkill)
		r.Get("/skills/{slug}", h.getSkill)
		r.Put("/skills/{slug}", h.updateSkill)

		// Providers
		r.Get("/providers", h.listProviders)
		r.Post("/providers", h.createProvider)
		r.Put("/providers/{id}", h.updateProvider)

		// Context documents
		r.Get("/contexts", h.listContexts)
		r.Post("/contexts", h.createContext)
		r.Get("/contexts/{id}", h.getContext)
		r.Put("/contexts/{id}", h.updateContext)
		r.Delete("/contexts/{id}", h.deleteContext)

		// Audit
		r.Get("/audit", h.queryAudit)
		r.Get("/audit/session/{sessionID}", h.sessionAuditTrail)
		r.Get("/audit/verify", h.verifyAuditChain)
		r.Get("/audit/export", h.exportAudit)
	})

	r.Get("/health", h.healthCheck)

	return r
}

// ==========================================================================
// Webhooks
// ==========================================================================

func (h *Handler) githubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := readAndRestore(r)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "failed to read body"})
		return
	}
	if err := VerifyGitHubSignature(h.GitHubWebhookSecret, body, r.Header.Get("X-Hub-Signature-256")); err != nil {
		writeJSON(w, 401, map[string]string{"error": err.Error()})
		return
	}

	var payload struct {
		Action      string `json:"action"`
		Number      int    `json:"number"`
		PullRequest struct {
			Title   string `json:"title"`
			HTMLURL string `json:"html_url"`
			Draft   bool   `json:"draft"`
			User    struct{ Login string } `json:"user"`
			Head    struct{ SHA, Ref string } `json:"head"`
			Base    struct{ SHA, Ref string } `json:"base"`
		} `json:"pull_request"`
		Repository struct{ FullName string `json:"full_name"` } `json:"repository"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid payload"})
		return
	}

	if payload.PullRequest.Draft {
		writeJSON(w, 200, map[string]any{"ignored": true, "reason": "draft PR"})
		return
	}

	session := h.createSession(r.Context(), types.ReviewSession{
		ExternalPRID:       fmt.Sprintf("github:%s#%d", payload.Repository.FullName, payload.Number),
		Platform:           types.PlatformGitHub,
		RepositoryFullName: payload.Repository.FullName,
		PRNumber:           payload.Number,
		PRTitle:            &payload.PullRequest.Title,
		PRAuthor:           &payload.PullRequest.User.Login,
		PRURL:              &payload.PullRequest.HTMLURL,
		HeadSHA:            payload.PullRequest.Head.SHA,
		BaseSHA:            payload.PullRequest.Base.SHA,
		HeadBranch:         &payload.PullRequest.Head.Ref,
		BaseBranch:         &payload.PullRequest.Base.Ref,
		TriggerSource:      types.TriggerWebhook,
		Status:             types.StatusQueued,
	})

	// Execute async
	if err := h.Enqueuer.EnqueueReview(r.Context(), session); err != nil {
		writeJSON(w, 500, map[string]string{"error": "failed to enqueue: " + err.Error()})
		return
	}

	writeJSON(w, 202, map[string]any{"sessionId": session.ID, "status": "queued"})
}

func (h *Handler) gitlabWebhook(w http.ResponseWriter, r *http.Request) {
	if err := VerifyGitLabToken(h.GitLabWebhookSecret, r.Header.Get("X-Gitlab-Token")); err != nil {
		writeJSON(w, 401, map[string]string{"error": err.Error()})
		return
	}

	var payload struct {
		ObjectKind string `json:"object_kind"`
		ObjectAttributes struct {
			IID          int    `json:"iid"`
			Title        string `json:"title"`
			URL          string `json:"url"`
			Action       string `json:"action"`
			SourceBranch string `json:"source_branch"`
			TargetBranch string `json:"target_branch"`
			LastCommit   struct{ ID string } `json:"last_commit"`
		} `json:"object_attributes"`
		User    struct{ Username string } `json:"user"`
		Project struct {
			PathWithNamespace string `json:"path_with_namespace"`
		} `json:"project"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid payload"})
		return
	}

	session := h.createSession(r.Context(), types.ReviewSession{
		ExternalPRID:       fmt.Sprintf("gitlab:%s!%d", payload.Project.PathWithNamespace, payload.ObjectAttributes.IID),
		Platform:           types.PlatformGitLab,
		RepositoryFullName: payload.Project.PathWithNamespace,
		PRNumber:           payload.ObjectAttributes.IID,
		PRTitle:            &payload.ObjectAttributes.Title,
		PRAuthor:           &payload.User.Username,
		PRURL:              &payload.ObjectAttributes.URL,
		HeadSHA:            payload.ObjectAttributes.LastCommit.ID,
		BaseSHA:            "",
		HeadBranch:         &payload.ObjectAttributes.SourceBranch,
		BaseBranch:         &payload.ObjectAttributes.TargetBranch,
		TriggerSource:      types.TriggerWebhook,
		Status:             types.StatusQueued,
	})

	if err := h.Enqueuer.EnqueueReview(r.Context(), session); err != nil {
		writeJSON(w, 500, map[string]string{"error": "failed to enqueue: " + err.Error()})
		return
	}
	writeJSON(w, 202, map[string]any{"sessionId": session.ID, "status": "queued"})
}

// bitbucketWebhook handles Bitbucket Cloud `pullrequest:created` and
// `pullrequest:updated` events. Bitbucket signs each delivery with an
// X-Hook-UUID matching the webhook's configured UUID.
func (h *Handler) bitbucketWebhook(w http.ResponseWriter, r *http.Request) {
	if err := VerifyGitLabToken(h.BitbucketWebhookUUID, r.Header.Get("X-Hook-UUID")); err != nil {
		writeJSON(w, 401, map[string]string{"error": err.Error()})
		return
	}

	var payload struct {
		PullRequest struct {
			ID          int    `json:"id"`
			Title       string `json:"title"`
			Links       struct {
				HTML struct{ Href string } `json:"html"`
			} `json:"links"`
			Author struct{ Username string } `json:"author"`
			Source struct {
				Branch struct{ Name string } `json:"branch"`
				Commit struct{ Hash string } `json:"commit"`
			} `json:"source"`
			Destination struct {
				Branch struct{ Name string } `json:"branch"`
				Commit struct{ Hash string } `json:"commit"`
			} `json:"destination"`
		} `json:"pullrequest"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid payload"})
		return
	}

	session := h.createSession(r.Context(), types.ReviewSession{
		ExternalPRID:       fmt.Sprintf("bitbucket:%s#%d", payload.Repository.FullName, payload.PullRequest.ID),
		Platform:           types.PlatformBitbucket,
		RepositoryFullName: payload.Repository.FullName,
		PRNumber:           payload.PullRequest.ID,
		PRTitle:            &payload.PullRequest.Title,
		PRAuthor:           &payload.PullRequest.Author.Username,
		PRURL:              &payload.PullRequest.Links.HTML.Href,
		HeadSHA:            payload.PullRequest.Source.Commit.Hash,
		BaseSHA:            payload.PullRequest.Destination.Commit.Hash,
		HeadBranch:         &payload.PullRequest.Source.Branch.Name,
		BaseBranch:         &payload.PullRequest.Destination.Branch.Name,
		TriggerSource:      types.TriggerWebhook,
		Status:             types.StatusQueued,
	})

	if err := h.Enqueuer.EnqueueReview(r.Context(), session); err != nil {
		writeJSON(w, 500, map[string]string{"error": "failed to enqueue: " + err.Error()})
		return
	}
	writeJSON(w, 202, map[string]any{"sessionId": session.ID, "status": "queued"})
}

// ==========================================================================
// Reviews
// ==========================================================================

func (h *Handler) triggerReview(w http.ResponseWriter, r *http.Request) {
	var req types.TriggerReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}

	externalID := fmt.Sprintf("%s:%s#%d", req.Platform, req.Repository, req.PRNumber)
	session := h.createSession(r.Context(), types.ReviewSession{
		ExternalPRID:       externalID,
		Platform:           req.Platform,
		RepositoryFullName: req.Repository,
		PRNumber:           req.PRNumber,
		HeadSHA:            req.HeadSHA,
		BaseSHA:            req.BaseSHA,
		DiffSnapshot:       req.Diff,
		TriggerSource:      req.TriggerSource,
		Status:             types.StatusQueued,
	})

	if err := h.Enqueuer.EnqueueReview(r.Context(), session); err != nil {
		writeJSON(w, 500, map[string]string{"error": "failed to enqueue: " + err.Error()})
		return
	}

	writeJSON(w, 202, map[string]any{
		"sessionId":          session.ID,
		"status":             "queued",
		"estimatedDurationMs": 30000,
	})
}

func (h *Handler) getSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "sessionID")

	s, err := h.scanSessionRow(r.Context(), id)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "session not found"})
		return
	}

	reports, err := h.loadReportsForSession(r.Context(), id)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}

	decisions, err := h.loadDecisionsForSession(r.Context(), id)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}

	// Progress: completed = number of reports written so far,
	// total = enabled agent count (used by polling callers like CI).
	total := len(reports)
	var enabledAgents int
	if err := h.DB.QueryRow(r.Context(),
		`SELECT count(*) FROM agents WHERE enabled = true`).Scan(&enabledAgents); err == nil {
		if enabledAgents > total {
			total = enabledAgents
		}
	}

	writeJSON(w, 200, map[string]any{
		"session":   s,
		"reports":   reports,
		"decisions": decisions,
		"progress": map[string]int{
			"completed": len(reports),
			"total":     total,
		},
	})
}

func (h *Handler) scanSessionRow(ctx context.Context, id string) (*types.ReviewSession, error) {
	row := h.DB.QueryRow(ctx,
		`SELECT id, external_pr_id, platform, repository_full_name, pr_number,
		        pr_title, pr_author, pr_url, head_sha, base_sha, base_branch, head_branch,
		        diff_snapshot, file_tree, status, composite_report, overall_verdict,
		        trigger_source, metadata, started_at, completed_at
		 FROM review_sessions WHERE id = $1`, id)

	var s types.ReviewSession
	err := row.Scan(
		&s.ID, &s.ExternalPRID, &s.Platform, &s.RepositoryFullName, &s.PRNumber,
		&s.PRTitle, &s.PRAuthor, &s.PRURL, &s.HeadSHA, &s.BaseSHA,
		&s.BaseBranch, &s.HeadBranch, &s.DiffSnapshot, &s.FileTree, &s.Status,
		&s.CompositeReport, &s.OverallVerdict, &s.TriggerSource, &s.Metadata,
		&s.StartedAt, &s.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (h *Handler) loadReportsForSession(ctx context.Context, sessionID string) ([]types.AgentReport, error) {
	rows, err := h.DB.Query(ctx,
		`SELECT id, session_id, agent_id, skill_slug, skill_version, model_used, provider_name,
		        prompt_hash, input_tokens, output_tokens, latency_ms, cost_estimate_usd,
		        raw_response, report_md, checklist_json, findings, verdict, created_at
		 FROM agent_reports WHERE session_id = $1 ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	reports := []types.AgentReport{}
	for rows.Next() {
		var rpt types.AgentReport
		if err := rows.Scan(
			&rpt.ID, &rpt.SessionID, &rpt.AgentID, &rpt.SkillSlug, &rpt.SkillVersion,
			&rpt.ModelUsed, &rpt.ProviderName, &rpt.PromptHash,
			&rpt.InputTokens, &rpt.OutputTokens, &rpt.LatencyMs, &rpt.CostEstimateUSD,
			&rpt.RawResponse, &rpt.ReportMD, &rpt.ChecklistJSON, &rpt.Findings,
			&rpt.Verdict, &rpt.CreatedAt,
		); err != nil {
			return nil, err
		}
		reports = append(reports, rpt)
	}
	return reports, rows.Err()
}

func (h *Handler) loadDecisionsForSession(ctx context.Context, sessionID string) ([]types.ApprovalDecision, error) {
	rows, err := h.DB.Query(ctx,
		`SELECT id, session_id, decision, engineer_id, engineer_name, engineer_email,
		        justification, overridden_verdicts, decided_at
		 FROM approval_decisions WHERE session_id = $1 ORDER BY decided_at DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	decisions := []types.ApprovalDecision{}
	for rows.Next() {
		var d types.ApprovalDecision
		if err := rows.Scan(
			&d.ID, &d.SessionID, &d.Decision, &d.EngineerID, &d.EngineerName,
			&d.EngineerEmail, &d.Justification, &d.OverriddenVerdicts, &d.DecidedAt,
		); err != nil {
			return nil, err
		}
		decisions = append(decisions, d)
	}
	return decisions, rows.Err()
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	rows, err := h.DB.Query(r.Context(),
		`SELECT id, external_pr_id, platform, repository_full_name, pr_number,
		        pr_title, pr_author, pr_url, head_sha, base_sha, status,
		        overall_verdict, started_at, completed_at
		 FROM review_sessions ORDER BY started_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	sessions := []map[string]any{}
	for rows.Next() {
		var id, extID, plat, repo, headSHA, baseSHA string
		var status types.ReviewStatus
		var prNum int
		var title, author, prURL *string
		var verdict *types.Verdict
		var started time.Time
		var completed *time.Time

		if err := rows.Scan(&id, &extID, &plat, &repo, &prNum,
			&title, &author, &prURL, &headSHA, &baseSHA, &status,
			&verdict, &started, &completed); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		sessions = append(sessions, map[string]any{
			"id":                   id,
			"external_pr_id":       extID,
			"platform":             plat,
			"repository_full_name": repo,
			"pr_number":            prNum,
			"pr_title":             title,
			"pr_author":            author,
			"pr_url":               prURL,
			"head_sha":             headSHA,
			"base_sha":             baseSHA,
			"status":               status,
			"overall_verdict":      verdict,
			"started_at":           started,
			"completed_at":         completed,
		})
	}

	writeJSON(w, 200, map[string]any{"sessions": sessions, "limit": limit, "offset": offset})
}

// rerunReview re-enqueues an existing session for review. Any prior agent
// reports are deleted (the orchestrator inserts fresh rows) and the session is
// reset to "queued". The diff snapshot is preserved so reruns evaluate the
// exact same code; if the user wants the latest commits they should trigger a
// fresh review instead.
func (h *Handler) rerunReview(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "sessionID")

	session, err := h.scanSessionRow(r.Context(), id)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "session not found"})
		return
	}

	if _, err := h.DB.Exec(r.Context(), "DELETE FROM agent_reports WHERE session_id = $1", id); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if _, err := h.DB.Exec(r.Context(),
		`UPDATE review_sessions SET status = 'queued', composite_report = NULL,
		  overall_verdict = NULL, completed_at = NULL WHERE id = $1`, id); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}

	session.Status = types.StatusQueued
	if err := h.Enqueuer.EnqueueReview(r.Context(), *session); err != nil {
		writeJSON(w, 500, map[string]string{"error": "failed to enqueue: " + err.Error()})
		return
	}

	h.Audit.Log(r.Context(), audit.Event{
		EventType: types.AuditSessionQueued,
		SessionID: &id,
		Actor:     "api",
		Payload:   map[string]any{"action": "rerun"},
	})

	writeJSON(w, 202, map[string]any{"sessionId": id, "status": "queued"})
}

// ==========================================================================
// Decisions
// ==========================================================================

func (h *Handler) submitDecision(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	var req types.SubmitDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}

	overridesJSON, _ := json.Marshal(req.OverriddenVerdicts)

	var id string
	err := h.DB.QueryRow(r.Context(),
		`INSERT INTO approval_decisions
		  (session_id, decision, engineer_id, engineer_name, engineer_email,
		   justification, overridden_verdicts)
		 VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		sessionID, req.Decision, req.EngineerID, req.EngineerName,
		req.EngineerEmail, req.Justification, overridesJSON,
	).Scan(&id)

	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}

	eventType := types.AuditDecisionApprove
	if req.Decision == types.DecisionRequestChanges {
		eventType = types.AuditDecisionChanges
	} else if req.Decision == types.DecisionDefer {
		eventType = types.AuditDecisionDefer
	}

	h.Audit.Log(r.Context(), audit.Event{
		EventType: eventType,
		SessionID: &sessionID,
		Actor:     req.EngineerID,
		Payload: map[string]any{
			"engineerName":  req.EngineerName,
			"decision":      req.Decision,
			"justification": req.Justification,
			"overrideCount": len(req.OverriddenVerdicts),
		},
	})

	writeJSON(w, 201, map[string]string{"id": id, "decision": string(req.Decision)})
}

func (h *Handler) getDecisions(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	decisions, err := h.loadDecisionsForSession(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"sessionId": sessionID, "decisions": decisions})
}

func (h *Handler) getDecisionsByEngineer(w http.ResponseWriter, r *http.Request) {
	engineerID := chi.URLParam(r, "engineerID")
	rows, err := h.DB.Query(r.Context(),
		`SELECT id, session_id, decision, engineer_id, engineer_name, engineer_email,
		        justification, overridden_verdicts, decided_at
		 FROM approval_decisions WHERE engineer_id = $1 ORDER BY decided_at DESC LIMIT 200`,
		engineerID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	decisions := []types.ApprovalDecision{}
	for rows.Next() {
		var d types.ApprovalDecision
		if err := rows.Scan(
			&d.ID, &d.SessionID, &d.Decision, &d.EngineerID, &d.EngineerName,
			&d.EngineerEmail, &d.Justification, &d.OverriddenVerdicts, &d.DecidedAt,
		); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		decisions = append(decisions, d)
	}
	writeJSON(w, 200, map[string]any{"engineerId": engineerID, "decisions": decisions})
}

// ==========================================================================
// Agents CRUD
// ==========================================================================

func (h *Handler) listAgents(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(r.Context(), `
		SELECT a.id, a.name, a.enabled, a.priority, a.temperature,
		       s.slug, s.name, p.name
		FROM agents a JOIN skills s ON a.skill_id = s.id
		JOIN llm_providers p ON a.provider_id = p.id
		ORDER BY a.priority`)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	agents := []map[string]any{}
	for rows.Next() {
		var id, name, skillSlug, skillName, provName string
		var enabled bool
		var priority int
		var temp float64
		rows.Scan(&id, &name, &enabled, &priority, &temp, &skillSlug, &skillName, &provName)
		agents = append(agents, map[string]any{
			"id": id, "name": name, "enabled": enabled, "priority": priority,
			"temperature": temp, "skill_slug": skillSlug, "skill_name": skillName,
			"provider_name": provName,
		})
	}
	writeJSON(w, 200, map[string]any{"agents": agents})
}

func (h *Handler) createAgent(w http.ResponseWriter, r *http.Request) {
	var req types.CreateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.Name == "" || req.SkillID == "" || req.ProviderID == "" {
		writeJSON(w, 400, map[string]string{"error": "name, skillId, and providerId are required"})
		return
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 4096
	}
	if req.Priority == 0 {
		req.Priority = 100
	}

	var id string
	err := h.DB.QueryRow(r.Context(),
		`INSERT INTO agents (name, skill_id, provider_id, model_override, temperature,
		  max_tokens, enabled, priority, config)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
		req.Name, req.SkillID, req.ProviderID, req.ModelOverride,
		req.Temperature, req.MaxTokens, req.Enabled, req.Priority,
		mustJSON(req.Config),
	).Scan(&id)

	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}

	for _, ctxID := range req.ContextIDs {
		h.DB.Exec(r.Context(), "INSERT INTO agent_context (agent_id, context_id) VALUES ($1,$2)", id, ctxID)
	}

	h.Audit.Log(r.Context(), audit.Event{
		EventType: types.AuditConfigCreated, AgentID: &id, Actor: "api",
		Payload: map[string]any{"name": req.Name},
	})
	writeJSON(w, 201, map[string]string{"id": id})
}

func (h *Handler) getAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var name, skillSlug, skillName, provName string
	var skillID, providerID string
	var modelOverride *string
	var enabled bool
	var priority, maxTokens int
	var temperature float64
	var configRaw json.RawMessage

	err := h.DB.QueryRow(r.Context(), `
		SELECT a.id, a.name, a.skill_id, a.provider_id, a.model_override,
		       a.temperature, a.max_tokens, a.enabled, a.priority, a.config,
		       s.slug, s.name, p.name
		FROM agents a
		JOIN skills s ON a.skill_id = s.id
		JOIN llm_providers p ON a.provider_id = p.id
		WHERE a.id = $1`, id,
	).Scan(&id, &name, &skillID, &providerID, &modelOverride,
		&temperature, &maxTokens, &enabled, &priority, &configRaw,
		&skillSlug, &skillName, &provName)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "agent not found"})
		return
	}

	// Eager-load context IDs so the dashboard can render the binding.
	contextIDs := []string{}
	rows, err := h.DB.Query(r.Context(),
		"SELECT context_id FROM agent_context WHERE agent_id = $1", id)
	if err == nil {
		for rows.Next() {
			var cid string
			if err := rows.Scan(&cid); err == nil {
				contextIDs = append(contextIDs, cid)
			}
		}
		rows.Close()
	}

	writeJSON(w, 200, map[string]any{
		"id":             id,
		"name":           name,
		"skill_id":       skillID,
		"provider_id":    providerID,
		"model_override": modelOverride,
		"temperature":    temperature,
		"max_tokens":     maxTokens,
		"enabled":        enabled,
		"priority":       priority,
		"config":         configRaw,
		"skill_slug":     skillSlug,
		"skill_name":     skillName,
		"provider_name":  provName,
		"context_ids":    contextIDs,
	})
}

func (h *Handler) updateAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Optional fields — only the ones present in the body get patched.
	var req struct {
		Name          *string             `json:"name"`
		ModelOverride *string             `json:"modelOverride"`
		Temperature   *float64            `json:"temperature"`
		MaxTokens     *int                `json:"maxTokens"`
		Enabled       *bool               `json:"enabled"`
		Priority      *int                `json:"priority"`
		Config        *types.AgentConfig  `json:"config"`
		ContextIDs    *[]string           `json:"contextIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}

	// Build a partial UPDATE using COALESCE so callers can patch any subset.
	tag, err := h.DB.Exec(r.Context(), `
		UPDATE agents SET
		  name           = COALESCE($2, name),
		  model_override = COALESCE($3, model_override),
		  temperature    = COALESCE($4, temperature),
		  max_tokens     = COALESCE($5, max_tokens),
		  enabled        = COALESCE($6, enabled),
		  priority       = COALESCE($7, priority),
		  config         = COALESCE($8::jsonb, config),
		  updated_at     = now()
		WHERE id = $1`,
		id, req.Name, req.ModelOverride, req.Temperature, req.MaxTokens,
		req.Enabled, req.Priority, jsonOrNil(req.Config),
	)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSON(w, 404, map[string]string{"error": "agent not found"})
		return
	}

	if req.ContextIDs != nil {
		// Re-bind contexts atomically: delete + re-insert. agent_context is
		// a small join table with no other state.
		if _, err := h.DB.Exec(r.Context(), "DELETE FROM agent_context WHERE agent_id = $1", id); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		for _, cid := range *req.ContextIDs {
			if _, err := h.DB.Exec(r.Context(),
				"INSERT INTO agent_context (agent_id, context_id) VALUES ($1,$2)", id, cid); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
		}
	}

	h.Audit.Log(r.Context(), audit.Event{
		EventType: types.AuditConfigUpdated,
		AgentID:   &id,
		Actor:     "api",
		Payload:   map[string]any{"id": id},
	})
	writeJSON(w, 200, map[string]any{"updated": true, "id": id})
}
func (h *Handler) disableAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tag, err := h.DB.Exec(r.Context(), "UPDATE agents SET enabled = false, updated_at = now() WHERE id = $1", id)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSON(w, 404, map[string]string{"error": "agent not found"})
		return
	}
	h.Audit.Log(r.Context(), audit.Event{
		EventType: types.AuditConfigUpdated,
		AgentID:   &id,
		Actor:     "api",
		Payload:   map[string]any{"action": "disable"},
	})
	writeJSON(w, 200, map[string]any{"disabled": true})
}

// ==========================================================================
// Skills CRUD
// ==========================================================================

func (h *Handler) listSkills(w http.ResponseWriter, r *http.Request) {
	rows, _ := h.DB.Query(r.Context(), "SELECT id, slug, name, version, is_builtin, tags FROM skills ORDER BY slug")
	defer rows.Close()
	skills := []map[string]any{}
	for rows.Next() {
		var id, slug, name string
		var version int
		var builtin bool
		var tags []string
		rows.Scan(&id, &slug, &name, &version, &builtin, &tags)
		skills = append(skills, map[string]any{
			"id": id, "slug": slug, "name": name, "version": version,
			"is_builtin": builtin, "tags": tags,
		})
	}
	writeJSON(w, 200, map[string]any{"skills": skills})
}

func (h *Handler) createSkill(w http.ResponseWriter, r *http.Request) {
	var req types.CreateSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.Slug == "" || req.Name == "" || req.SystemPrompt == "" || req.ChecklistTemplate == "" {
		writeJSON(w, 400, map[string]string{"error": "slug, name, systemPrompt, and checklistTemplate are required"})
		return
	}
	if req.Version <= 0 {
		req.Version = 1
	}
	if req.Tags == nil {
		req.Tags = []string{}
	}

	var id string
	err := h.DB.QueryRow(r.Context(),
		`INSERT INTO skills (slug, name, version, system_prompt, checklist_template, output_schema, tags, is_builtin)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (slug, version) DO UPDATE SET
		   name = EXCLUDED.name,
		   system_prompt = EXCLUDED.system_prompt,
		   checklist_template = EXCLUDED.checklist_template,
		   output_schema = EXCLUDED.output_schema,
		   tags = EXCLUDED.tags,
		   updated_at = now()
		 RETURNING id`,
		req.Slug, req.Name, req.Version, req.SystemPrompt, req.ChecklistTemplate,
		req.OutputSchema, req.Tags, true,
	).Scan(&id)

	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}

	h.Audit.Log(r.Context(), audit.Event{
		EventType: types.AuditSkillCreated,
		Actor:     "api",
		Payload:   map[string]any{"slug": req.Slug, "version": req.Version},
	})

	writeJSON(w, 201, map[string]any{"id": id, "slug": req.Slug, "version": req.Version})
}

func (h *Handler) getSkill(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	var id, name, prompt, tmpl string
	var version int
	err := h.DB.QueryRow(r.Context(),
		"SELECT id, name, version, system_prompt, checklist_template FROM skills WHERE slug = $1 ORDER BY version DESC LIMIT 1",
		slug).Scan(&id, &name, &version, &prompt, &tmpl)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"id": id, "slug": slug, "name": name, "version": version,
		"systemPrompt": prompt, "checklistTemplate": tmpl,
	})
}

// updateSkill upserts the skill identified by slug. If the body specifies a
// version that already exists, that version row is updated in place; otherwise
// a new version row is inserted (skills are versioned by `(slug, version)`).
func (h *Handler) updateSkill(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	var req types.CreateSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	// The URL param wins so callers can't rename a skill via PUT (rename means
	// "register a new skill").
	req.Slug = slug
	if req.Name == "" || req.SystemPrompt == "" || req.ChecklistTemplate == "" {
		writeJSON(w, 400, map[string]string{"error": "name, systemPrompt, and checklistTemplate are required"})
		return
	}
	if req.Version <= 0 {
		// Default to the next version number for this slug.
		var maxV int
		_ = h.DB.QueryRow(r.Context(),
			"SELECT COALESCE(MAX(version), 0) FROM skills WHERE slug = $1", slug).Scan(&maxV)
		req.Version = maxV + 1
	}
	if req.Tags == nil {
		req.Tags = []string{}
	}

	var id string
	err := h.DB.QueryRow(r.Context(),
		`INSERT INTO skills (slug, name, version, system_prompt, checklist_template, output_schema, tags, is_builtin)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (slug, version) DO UPDATE SET
		   name = EXCLUDED.name,
		   system_prompt = EXCLUDED.system_prompt,
		   checklist_template = EXCLUDED.checklist_template,
		   output_schema = EXCLUDED.output_schema,
		   tags = EXCLUDED.tags,
		   updated_at = now()
		 RETURNING id`,
		req.Slug, req.Name, req.Version, req.SystemPrompt, req.ChecklistTemplate,
		req.OutputSchema, req.Tags, false,
	).Scan(&id)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}

	h.Audit.Log(r.Context(), audit.Event{
		EventType: types.AuditSkillUpdated,
		Actor:     "api",
		Payload:   map[string]any{"slug": req.Slug, "version": req.Version},
	})
	writeJSON(w, 200, map[string]any{"id": id, "slug": slug, "version": req.Version, "updated": true})
}

// ==========================================================================
// Providers CRUD
// ==========================================================================

func (h *Handler) listProviders(w http.ResponseWriter, r *http.Request) {
	rows, _ := h.DB.Query(r.Context(),
		"SELECT id, name, base_url, auth_method, default_model, models, config FROM llm_providers ORDER BY name")
	defer rows.Close()
	providers := []map[string]any{}
	for rows.Next() {
		var id, name, url, auth, model string
		var models []string
		var config json.RawMessage
		rows.Scan(&id, &name, &url, &auth, &model, &models, &config)
		if models == nil {
			models = []string{}
		}
		providers = append(providers, map[string]any{
			"id": id, "name": name, "base_url": url, "auth_method": auth,
			"default_model": model, "models": models, "config": config,
		})
	}
	writeJSON(w, 200, map[string]any{"providers": providers})
}

func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) {
	var req types.CreateProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.Name == "" || req.BaseURL == "" || req.DefaultModel == "" || req.AuthMethod == "" {
		writeJSON(w, 400, map[string]string{"error": "name, baseUrl, authMethod, defaultModel are required"})
		return
	}
	if req.Models == nil {
		req.Models = []string{}
	}

	var id string
	err := h.DB.QueryRow(r.Context(),
		`INSERT INTO llm_providers (name, base_url, auth_method, api_key_ref, default_model, models, config)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (name) DO UPDATE SET
		   base_url = EXCLUDED.base_url,
		   auth_method = EXCLUDED.auth_method,
		   api_key_ref = EXCLUDED.api_key_ref,
		   default_model = EXCLUDED.default_model,
		   models = EXCLUDED.models,
		   config = EXCLUDED.config,
		   updated_at = now()
		 RETURNING id`,
		req.Name, req.BaseURL, req.AuthMethod, req.APIKeyRef, req.DefaultModel,
		req.Models, mustJSON(req.Config),
	).Scan(&id)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}

	h.Audit.Log(r.Context(), audit.Event{
		EventType: types.AuditProviderCreated,
		Actor:     "api",
		Payload:   map[string]any{"name": req.Name, "defaultModel": req.DefaultModel},
	})
	writeJSON(w, 201, map[string]any{"id": id, "name": req.Name})
}

// updateProvider patches the provider row identified by id. Unset fields are
// preserved so callers can change just the model or rate limit without
// re-sending the full provider config.
func (h *Handler) updateProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		Name         *string               `json:"name"`
		BaseURL      *string               `json:"baseUrl"`
		AuthMethod   *types.AuthMethod     `json:"authMethod"`
		APIKeyRef    *string               `json:"apiKeyRef"`
		DefaultModel *string               `json:"defaultModel"`
		Models       *[]string             `json:"models"`
		Config       *types.ProviderConfig `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}

	tag, err := h.DB.Exec(r.Context(), `
		UPDATE llm_providers SET
		  name          = COALESCE($2, name),
		  base_url      = COALESCE($3, base_url),
		  auth_method   = COALESCE($4, auth_method),
		  api_key_ref   = COALESCE($5, api_key_ref),
		  default_model = COALESCE($6, default_model),
		  models        = COALESCE($7, models),
		  config        = COALESCE($8::jsonb, config),
		  updated_at    = now()
		WHERE id = $1`,
		id, req.Name, req.BaseURL, req.AuthMethod, req.APIKeyRef,
		req.DefaultModel, req.Models, jsonOrNil(req.Config),
	)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSON(w, 404, map[string]string{"error": "provider not found"})
		return
	}

	h.Audit.Log(r.Context(), audit.Event{
		EventType: types.AuditProviderUpdated,
		Actor:     "api",
		Payload:   map[string]any{"id": id},
	})
	writeJSON(w, 200, map[string]any{"updated": true, "id": id})
}

// ==========================================================================
// Context documents
// ==========================================================================

func (h *Handler) listContexts(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(r.Context(),
		`SELECT id, name, doc_type, tags, created_at FROM context_documents ORDER BY name`)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var id, name, docType string
		var tags []string
		var createdAt time.Time
		if err := rows.Scan(&id, &name, &docType, &tags, &createdAt); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if tags == nil {
			tags = []string{}
		}
		out = append(out, map[string]any{
			"id": id, "name": name, "doc_type": docType,
			"tags": tags, "created_at": createdAt,
		})
	}
	// listContexts intentionally omits `content` so the dashboard's index
	// stays small. Use GET /api/contexts/{id} to fetch the body.
	writeJSON(w, 200, map[string]any{"contexts": out})
}

func (h *Handler) getContext(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var c types.ContextDocument
	err := h.DB.QueryRow(r.Context(),
		`SELECT id, name, content, doc_type, tags, created_at
		 FROM context_documents WHERE id = $1`, id,
	).Scan(&c.ID, &c.Name, &c.Content, &c.DocType, &c.Tags, &c.CreatedAt)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "context not found"})
		return
	}
	if c.Tags == nil {
		c.Tags = []string{}
	}
	writeJSON(w, 200, c)
}

func (h *Handler) createContext(w http.ResponseWriter, r *http.Request) {
	var req types.CreateContextRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.Name == "" || req.Content == "" || req.DocType == "" {
		writeJSON(w, 400, map[string]string{"error": "name, content, and docType are required"})
		return
	}
	if !validDocType(req.DocType) {
		writeJSON(w, 400, map[string]string{"error": "docType must be guideline|non-negotiable|reference|example"})
		return
	}
	if req.Tags == nil {
		req.Tags = []string{}
	}

	var id string
	err := h.DB.QueryRow(r.Context(),
		`INSERT INTO context_documents (name, content, doc_type, tags)
		 VALUES ($1,$2,$3,$4) RETURNING id`,
		req.Name, req.Content, req.DocType, req.Tags,
	).Scan(&id)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}

	h.Audit.Log(r.Context(), audit.Event{
		EventType: types.AuditConfigCreated,
		Actor:     "api",
		Payload:   map[string]any{"resource": "context", "id": id, "name": req.Name},
	})
	writeJSON(w, 201, map[string]any{"id": id, "name": req.Name})
}

func (h *Handler) updateContext(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		Name    *string   `json:"name"`
		Content *string   `json:"content"`
		DocType *string   `json:"docType"`
		Tags    *[]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.DocType != nil && !validDocType(*req.DocType) {
		writeJSON(w, 400, map[string]string{"error": "docType must be guideline|non-negotiable|reference|example"})
		return
	}

	tag, err := h.DB.Exec(r.Context(), `
		UPDATE context_documents SET
		  name     = COALESCE($2, name),
		  content  = COALESCE($3, content),
		  doc_type = COALESCE($4, doc_type),
		  tags     = COALESCE($5, tags)
		WHERE id = $1`,
		id, req.Name, req.Content, req.DocType, req.Tags,
	)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSON(w, 404, map[string]string{"error": "context not found"})
		return
	}
	h.Audit.Log(r.Context(), audit.Event{
		EventType: types.AuditConfigUpdated,
		Actor:     "api",
		Payload:   map[string]any{"resource": "context", "id": id},
	})
	writeJSON(w, 200, map[string]any{"updated": true, "id": id})
}

func (h *Handler) deleteContext(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Cascade-delete the m:n bindings; the document's only consumers are
	// agents via agent_context, so this is safe.
	if _, err := h.DB.Exec(r.Context(), "DELETE FROM agent_context WHERE context_id = $1", id); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	tag, err := h.DB.Exec(r.Context(), "DELETE FROM context_documents WHERE id = $1", id)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSON(w, 404, map[string]string{"error": "context not found"})
		return
	}
	h.Audit.Log(r.Context(), audit.Event{
		EventType: types.AuditConfigUpdated,
		Actor:     "api",
		Payload:   map[string]any{"resource": "context", "id": id, "action": "delete"},
	})
	writeJSON(w, 200, map[string]any{"deleted": true})
}

func validDocType(s string) bool {
	switch s {
	case "guideline", "non-negotiable", "reference", "example":
		return true
	}
	return false
}

// ==========================================================================
// Audit
// ==========================================================================

func (h *Handler) queryAudit(w http.ResponseWriter, r *http.Request) {
	params := audit.QueryParams{
		SessionID: r.URL.Query().Get("sessionId"),
		EventType: r.URL.Query().Get("eventType"),
		Actor:     r.URL.Query().Get("actor"),
		Limit:     100,
	}
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		params.Limit = l
	}
	if o, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil {
		params.Offset = o
	}

	entries, err := h.Audit.Query(r.Context(), params)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"entries": entries, "count": len(entries)})
}

func (h *Handler) sessionAuditTrail(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	entries, err := h.Audit.SessionTrail(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"sessionId": sessionID, "entries": entries})
}

func (h *Handler) verifyAuditChain(w http.ResponseWriter, r *http.Request) {
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if from == 0 { from = 1 }
	if to == 0 { to = 1<<53 }

	result, err := h.Audit.VerifyChain(r.Context(), from, to)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, result)
}

func (h *Handler) exportAudit(w http.ResponseWriter, r *http.Request) {
	entries, _ := h.Audit.Query(r.Context(), audit.QueryParams{Limit: 10000})
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", "attachment; filename=phalanx-audit.jsonl")
	for _, e := range entries {
		line, _ := json.Marshal(e)
		w.Write(line)
		w.Write([]byte("\n"))
	}
}

// ==========================================================================
// Health
// ==========================================================================

func (h *Handler) healthCheck(w http.ResponseWriter, r *http.Request) {
	dbOk := h.DB.Ping(r.Context()) == nil
	status := "healthy"
	if !dbOk {
		status = "degraded"
	}
	writeJSON(w, 200, map[string]any{"status": status, "database": dbOk})
}

// ==========================================================================
// Helpers
// ==========================================================================

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// jsonOrNil returns nil when v is a nil pointer (so the caller's COALESCE-based
// UPDATE preserves the existing column) and the marshalled bytes otherwise.
func jsonOrNil(v any) []byte {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr && rv.IsNil() {
		return nil
	}
	b, _ := json.Marshal(v)
	return b
}

func (h *Handler) createSession(ctx context.Context, s types.ReviewSession) types.ReviewSession {
	row := h.DB.QueryRow(ctx,
		`INSERT INTO review_sessions
		  (external_pr_id, platform, repository_full_name, pr_number, pr_title,
		   pr_author, pr_url, head_sha, base_sha, base_branch, head_branch,
		   diff_snapshot, trigger_source, status)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		 RETURNING id, started_at`,
		s.ExternalPRID, s.Platform, s.RepositoryFullName, s.PRNumber,
		s.PRTitle, s.PRAuthor, s.PRURL, s.HeadSHA, s.BaseSHA,
		s.BaseBranch, s.HeadBranch, s.DiffSnapshot, s.TriggerSource, s.Status)

	row.Scan(&s.ID, &s.StartedAt)

	h.Audit.Log(ctx, audit.Event{
		EventType: types.AuditSessionCreated,
		SessionID: &s.ID,
		Actor:     string(s.TriggerSource),
		Payload:   map[string]any{"platform": s.Platform, "repo": s.RepositoryFullName, "pr": s.PRNumber},
	})

	return s
}
