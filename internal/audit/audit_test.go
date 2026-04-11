package audit

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/phalanx-ai/phalanx/internal/types"
)

// openTestDB connects to PHALANX_TEST_DATABASE_URL. Tests that need a real
// Postgres are skipped when the variable is unset — this keeps `go test ./...`
// green in environments without the compose stack running.
func openTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("PHALANX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PHALANX_TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestHashChain_ValidInsertsVerify(t *testing.T) {
	pool := openTestDB(t)
	ctx := context.Background()

	// Clean slate
	if _, err := pool.Exec(ctx, "TRUNCATE audit_log RESTART IDENTITY"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	l := New(pool, true)
	for i := 0; i < 5; i++ {
		l.Log(ctx, Event{
			EventType: types.AuditLLMRequest,
			Actor:     "test",
			Payload:   map[string]any{"i": i},
		})
	}

	result, err := l.VerifyChain(ctx, 1, 1<<20)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !result.Valid {
		t.Errorf("chain reported invalid, first broken ID=%v", result.FirstBrokenID)
	}
	if result.CheckedCount != 5 {
		t.Errorf("checked %d rows, want 5", result.CheckedCount)
	}
}

func TestHashChain_DetectsTampering(t *testing.T) {
	pool := openTestDB(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, "TRUNCATE audit_log RESTART IDENTITY"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	l := New(pool, true)
	for i := 0; i < 4; i++ {
		l.Log(ctx, Event{
			EventType: types.AuditSessionCreated,
			Actor:     "test",
			Payload:   map[string]any{"i": i},
		})
	}

	// Mutate row 2 directly — chain should break.
	if _, err := pool.Exec(ctx,
		`UPDATE audit_log SET payload = '{"i": 999}'::jsonb WHERE id = 2`); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	result, err := l.VerifyChain(ctx, 1, 1<<20)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if result.Valid {
		t.Errorf("expected tampering to be detected")
	}
	if result.FirstBrokenID == nil || *result.FirstBrokenID != 2 {
		t.Errorf("expected first broken ID = 2, got %v", result.FirstBrokenID)
	}
}
