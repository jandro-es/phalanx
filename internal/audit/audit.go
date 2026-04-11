// Package audit provides an append-only audit logger backed by PostgreSQL.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/phalanx-ai/phalanx/internal/types"
)

// Logger writes immutable audit records. The DB role has INSERT+SELECT only.
//
// When hash chaining is enabled, all chained writes are serialized through
// chainMu so concurrent callers can't both read the same prev_hash and
// insert records that break the chain.
type Logger struct {
	db        *pgxpool.Pool
	hashChain bool
	chainMu   sync.Mutex
}

// New creates an audit logger.
func New(db *pgxpool.Pool, hashChain bool) *Logger {
	return &Logger{db: db, hashChain: hashChain}
}

// Event is an audit event to log.
type Event struct {
	EventType types.AuditEventType
	SessionID *string
	AgentID   *string
	Actor     string
	Payload   map[string]any
}

// Log writes an audit record. Never returns an error to callers — logs internally.
func (l *Logger) Log(ctx context.Context, e Event) {
	payload, _ := json.Marshal(e.Payload)

	if l.hashChain {
		l.logWithChain(ctx, e, payload)
		return
	}

	_, err := l.db.Exec(ctx,
		`INSERT INTO audit_log (event_type, session_id, agent_id, actor, payload)
		 VALUES ($1, $2, $3, $4, $5::jsonb)`,
		e.EventType, e.SessionID, e.AgentID, e.Actor, payload,
	)
	if err != nil {
		fmt.Printf("[AUDIT ERROR] failed to write audit log: %v\n", err)
	}
}

func (l *Logger) logWithChain(ctx context.Context, e Event, payload []byte) {
	// Serialize all chained writes so concurrent callers don't race on prev_hash.
	l.chainMu.Lock()
	defer l.chainMu.Unlock()

	// Fetch previous hash
	var prevHash string
	row := l.db.QueryRow(ctx,
		`SELECT COALESCE(payload_hash, 'GENESIS') FROM audit_log ORDER BY id DESC LIMIT 1`)
	if err := row.Scan(&prevHash); err != nil {
		prevHash = "GENESIS"
	}

	// Truncate to microseconds so the write-side timestamp round-trips
	// through PostgreSQL's timestamptz (microsecond precision) unchanged.
	now := time.Now().UTC().Truncate(time.Microsecond)
	nowStr := now.Format(time.RFC3339Nano)

	// Canonicalize the payload so the hash survives jsonb's key reordering
	// and whitespace normalization.
	canon, err := canonicalizeJSON(payload)
	if err != nil {
		fmt.Printf("[AUDIT ERROR] failed to canonicalize payload: %v\n", err)
		return
	}

	chainInput := fmt.Sprintf("%s|%s|%s|%s|%s", prevHash, e.EventType, e.Actor, canon, nowStr)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(chainInput)))

	if _, err := l.db.Exec(ctx,
		`INSERT INTO audit_log (event_type, session_id, agent_id, actor, payload, payload_hash, prev_hash, created_at)
		 VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)`,
		e.EventType, e.SessionID, e.AgentID, e.Actor, canon, hash, prevHash, now,
	); err != nil {
		fmt.Printf("[AUDIT ERROR] failed to write chained audit log: %v\n", err)
	}
}

// canonicalizeJSON returns a deterministic byte representation of the given
// JSON: keys sorted alphabetically, whitespace removed. Re-running this on
// the jsonb-normalized bytes read back from Postgres must produce the same
// output, otherwise hash chain verification can't work.
func canonicalizeJSON(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// Query returns audit entries matching the given filters.
func (l *Logger) Query(ctx context.Context, params QueryParams) ([]types.AuditEntry, error) {
	query := `SELECT id, event_type, session_id, agent_id, actor, payload, payload_hash, prev_hash, created_at FROM audit_log WHERE 1=1`
	args := []any{}
	idx := 1

	if params.SessionID != "" {
		query += fmt.Sprintf(" AND session_id = $%d", idx)
		args = append(args, params.SessionID)
		idx++
	}
	if params.EventType != "" {
		query += fmt.Sprintf(" AND event_type = $%d", idx)
		args = append(args, params.EventType)
		idx++
	}
	if params.Actor != "" {
		query += fmt.Sprintf(" AND actor = $%d", idx)
		args = append(args, params.Actor)
		idx++
	}
	if params.From != nil {
		query += fmt.Sprintf(" AND created_at >= $%d", idx)
		args = append(args, *params.From)
		idx++
	}
	if params.To != nil {
		query += fmt.Sprintf(" AND created_at <= $%d", idx)
		args = append(args, *params.To)
		idx++
	}

	query += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d OFFSET $%d", idx, idx+1)
	if params.Limit == 0 {
		params.Limit = 100
	}
	args = append(args, params.Limit, params.Offset)

	rows, err := l.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := []types.AuditEntry{}
	for rows.Next() {
		var e types.AuditEntry
		if err := rows.Scan(&e.ID, &e.EventType, &e.SessionID, &e.AgentID, &e.Actor,
			&e.Payload, &e.PayloadHash, &e.PrevHash, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// SessionTrail returns the full audit trail for a session, ordered chronologically.
func (l *Logger) SessionTrail(ctx context.Context, sessionID string) ([]types.AuditEntry, error) {
	return l.Query(ctx, QueryParams{SessionID: sessionID, Limit: 10000})
}

// VerifyChain checks hash chain integrity between two record IDs.
func (l *Logger) VerifyChain(ctx context.Context, fromID, toID int64) (*ChainVerification, error) {
	if !l.hashChain {
		return &ChainVerification{Valid: true, CheckedCount: 0}, nil
	}

	rows, err := l.db.Query(ctx,
		`SELECT id, event_type, actor, payload, payload_hash, prev_hash, created_at
		 FROM audit_log WHERE id >= $1 AND id <= $2 ORDER BY id ASC`, fromID, toID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	checked := 0
	firstRow := true
	var prevHash string

	for rows.Next() {
		var id int64
		var eventType, actor, payloadHash string
		var rowPrevHash *string
		var payload json.RawMessage
		var createdAt time.Time

		if err := rows.Scan(&id, &eventType, &actor, &payload, &payloadHash, &rowPrevHash, &createdAt); err != nil {
			return nil, err
		}

		if firstRow {
			if rowPrevHash != nil {
				prevHash = *rowPrevHash
			} else {
				prevHash = "GENESIS"
			}
			firstRow = false
		}

		canon, err := canonicalizeJSON(payload)
		if err != nil {
			return &ChainVerification{Valid: false, CheckedCount: checked, FirstBrokenID: &id}, nil
		}

		createdAtStr := createdAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
		chainInput := fmt.Sprintf("%s|%s|%s|%s|%s", prevHash, eventType, actor, canon, createdAtStr)
		expected := fmt.Sprintf("%x", sha256.Sum256([]byte(chainInput)))

		if payloadHash != expected {
			return &ChainVerification{Valid: false, CheckedCount: checked, FirstBrokenID: &id}, nil
		}
		prevHash = payloadHash
		checked++
	}

	return &ChainVerification{Valid: true, CheckedCount: checked}, nil
}

// QueryParams for filtering audit entries.
type QueryParams struct {
	SessionID string
	AgentID   string
	EventType string
	Actor     string
	From      *time.Time
	To        *time.Time
	Limit     int
	Offset    int
}

// ChainVerification is the result of a hash chain integrity check.
type ChainVerification struct {
	Valid         bool   `json:"valid"`
	CheckedCount int    `json:"checkedCount"`
	FirstBrokenID *int64 `json:"firstBrokenId,omitempty"`
}
