// Package dag runs deterministic dependency graphs with bounded retries.
//
// Nodes execute only after every dependency has succeeded. Retry callers can
// supply an IdempotentStore: a result committed once is never committed again,
// even if the commit-acknowledging attempt returns an error.
package dag

import (
	"context"
	"sync"
	"time"

	"github.com/zld/avpipe/internal/fault"
)

// Policy configures local retries.
type Policy struct {
	// MaxAttempts includes the initial attempt.
	MaxAttempts int
	// Backoff is the fixed wait between attempts in tests and local runs.
	Backoff time.Duration
}

// Normalize ensures at least one attempt.
func (p Policy) Normalize() Policy {
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	if p.Backoff < 0 {
		p.Backoff = 0
	}
	return p
}

// Sleeper waits unless ctx is canceled. It is a field so tests need not sleep.
type Sleeper func(ctx context.Context, d time.Duration) error

// IdempotentStore is an in-memory exactly-once result map.
type IdempotentStore struct {
	mu      sync.Mutex
	values  map[string][]byte
	commits int
}

// NewIdempotentStore creates an empty store.
func NewIdempotentStore() *IdempotentStore {
	return &IdempotentStore{values: make(map[string][]byte)}
}

// Commit stores key once. A repeated commit reports alreadyCommitted=true and
// does not replace or duplicate the original value.
func (s *IdempotentStore) Commit(key string, value []byte) (alreadyCommitted bool, err error) {
	if key == "" {
		return false, fault.New(fault.KindInvalid, "idempotency key is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.values[key]; ok {
		return true, nil
	}
	cp := append([]byte(nil), value...)
	s.values[key] = cp
	s.commits++
	return false, nil
}

// Get returns a committed value.
func (s *IdempotentStore) Get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	return append([]byte(nil), v...), ok
}

// Commits returns the number of first-time commits.
func (s *IdempotentStore) Commits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commits
}

// Retry runs fn until it succeeds or attempts are exhausted. When store and
// key are supplied, a value committed by an earlier ambiguous attempt ends
// the operation successfully and prevents a second commit.
func Retry(ctx context.Context, store *IdempotentStore, key string, policy Policy, fn func(attempt int) error) (int, error) {
	policy = policy.Normalize()
	var lastErr error

	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return attempt, fault.Wrap(fault.KindCanceled, err, "retry canceled")
		}
		if store != nil && key != "" {
			if _, ok := store.Get(key); ok {
				return attempt - 1, nil
			}
		}

		lastErr = fn(attempt)
		if lastErr == nil {
			return attempt, nil
		}
		if ctx.Err() != nil {
			return attempt, fault.Wrap(fault.KindCanceled, ctx.Err(), "retry canceled")
		}
		if attempt == policy.MaxAttempts {
			break
		}
		if policy.Backoff > 0 {
			if err := sleep(ctx, policy.Backoff); err != nil {
				return attempt, fault.Wrap(fault.KindCanceled, err, "retry backoff canceled")
			}
		}
	}
	return policy.MaxAttempts, fault.Wrap(fault.KindDependency, lastErr, "all %d attempts exhausted", policy.MaxAttempts)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
