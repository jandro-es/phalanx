package queue

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/phalanx-ai/phalanx/internal/types"
)

// startMiniredis spins up an in-process Redis (no docker required) and returns
// a redis:// URL that points at it.
func startMiniredis(t *testing.T) string {
	t.Helper()
	mr := miniredis.RunT(t)
	return fmt.Sprintf("redis://%s/0", mr.Addr())
}

// loadByID is a minimal session loader the worker can call. We embed the
// session ID into the test name so the worker has something distinguishable
// to compare against.
func loadByID(_ context.Context, id string) (*types.ReviewSession, error) {
	return &types.ReviewSession{ID: id, HeadSHA: "deadbeef"}, nil
}

func TestQueue_EnqueueDispatch(t *testing.T) {
	url := startMiniredis(t)

	client, err := NewClient(Options{RedisURL: url}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	var got int32
	gotID := make(chan string, 1)

	server, err := NewServer(Options{RedisURL: url, Concurrency: 2},
		func(ctx context.Context, s types.ReviewSession) error {
			atomic.AddInt32(&got, 1)
			select {
			case gotID <- s.ID:
			default:
			}
			return nil
		},
		loadByID,
	)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(server.Shutdown)

	if err := client.EnqueueReview(context.Background(),
		types.ReviewSession{ID: "session-abc"}); err != nil {
		t.Fatalf("EnqueueReview: %v", err)
	}

	select {
	case id := <-gotID:
		if id != "session-abc" {
			t.Fatalf("worker got id %q, want session-abc", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("worker never picked up the task (got=%d)", atomic.LoadInt32(&got))
	}
}

// TestQueue_HandlerErrorDoesNotCrashWorker exercises the failure path: a
// handler that returns an error must not panic, the worker must keep running,
// and a follow-up successful task must still be dispatched. (asynq's actual
// retry scheduling runs on >1s intervals which would make a strict
// "retry was invoked" assertion flaky in unit time, so we cover *that*
// behaviour at the integration level only.)
func TestQueue_HandlerErrorDoesNotCrashWorker(t *testing.T) {
	url := startMiniredis(t)

	client, err := NewClient(Options{RedisURL: url, MaxRetries: 0}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	var calls int32
	gotSecond := make(chan struct{}, 1)

	server, err := NewServer(
		Options{RedisURL: url, Concurrency: 1, MaxRetries: 0},
		func(ctx context.Context, s types.ReviewSession) error {
			n := atomic.AddInt32(&calls, 1)
			if s.ID == "boom" {
				return errors.New("transient")
			}
			if n >= 1 {
				select {
				case gotSecond <- struct{}{}:
				default:
				}
			}
			return nil
		},
		loadByID,
	)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(server.Shutdown)

	// First task fails — must not bring down the worker.
	if err := client.EnqueueReview(context.Background(),
		types.ReviewSession{ID: "boom"}); err != nil {
		t.Fatalf("Enqueue boom: %v", err)
	}
	// Second task should still get processed.
	if err := client.EnqueueReview(context.Background(),
		types.ReviewSession{ID: "ok"}); err != nil {
		t.Fatalf("Enqueue ok: %v", err)
	}

	select {
	case <-gotSecond:
	case <-time.After(3 * time.Second):
		t.Fatalf("worker never processed second task (calls=%d) — handler error likely killed the worker",
			atomic.LoadInt32(&calls))
	}
}

func TestQueue_NewServerRequiresHandler(t *testing.T) {
	if _, err := NewServer(Options{RedisURL: "redis://127.0.0.1:6379"}, nil, loadByID); err == nil {
		t.Fatal("nil handler should be rejected")
	}
	if _, err := NewServer(Options{RedisURL: "redis://127.0.0.1:6379"},
		func(_ context.Context, _ types.ReviewSession) error { return nil }, nil); err == nil {
		t.Fatal("nil loader should be rejected")
	}
}

func TestQueue_NewClientBadRedisURL(t *testing.T) {
	if _, err := NewClient(Options{RedisURL: "not a real url"}, nil); err == nil {
		t.Fatal("invalid redis URL should be rejected")
	}
}
