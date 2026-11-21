// Package replay keeps a concurrency-safe ledger of license nonces so that
// a replayed license can never pass validation twice within one process.
package replay

import "sync"

// Ledger records consumed nonces. The zero value is not usable; construct
// one with New.
type Ledger struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// New returns an empty ledger.
func New() *Ledger {
	return &Ledger{seen: map[string]struct{}{}}
}

// Consume atomically records a nonce and reports whether it was previously
// unused. Only the first concurrent caller for a given nonce receives true;
// every replay receives false.
func (l *Ledger) Consume(nonce string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.seen[nonce]; ok {
		return false
	}
	l.seen[nonce] = struct{}{}
	return true
}

// Len returns the number of distinct consumed nonces.
func (l *Ledger) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.seen)
}

// Seed marks a nonce as already consumed, simulating a license delivered by
// an earlier, completed request.
func (l *Ledger) Seed(nonce string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen[nonce] = struct{}{}
}
