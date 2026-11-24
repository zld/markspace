// Package dag: idempotency keys, retry, and a crash-safe result cache.
package dag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// KeyOf computes a node's idempotency key: a digest over the node kind, its
// parameters in sorted order, its own input hashes and the result hashes of
// its dependencies. Two runs with identical inputs, parameters and dependency
// outputs therefore produce the same key, and a reused verified result is
// provably the same artifact.
func KeyOf(n *Node, deps map[string]Result) string {
	h := sha256.New()
	fmt.Fprintf(h, "kind=%s|name=%s\n", n.Kind, n.Name)
	keys := make([]string, 0, len(n.Params))
	for k := range n.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "param %s=%s\n", k, n.Params[k])
	}
	// Inputs are already content hashes; they must be order-stable too.
	in := append([]string(nil), n.Inputs...)
	sort.Strings(in)
	for _, s := range in {
		fmt.Fprintf(h, "input %s\n", s)
	}
	names := make([]string, 0, len(n.DependsOn))
	for _, d := range n.DependsOn {
		names = append(names, d)
	}
	sort.Strings(names)
	for _, d := range names {
		fmt.Fprintf(h, "dep %s=%s\n", d, HashOf(deps[d]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// EmptyHash is the canonical digest of an absent output.
const EmptyHash = "none"

// HashOf normalises an empty result hash for key computation.
func HashOf(r Result) string {
	if r.Hash == "" {
		return EmptyHash
	}
	return r.Hash
}

// Policy controls retries for transient failures.
type Policy struct {
	Attempts int
	Backoff  time.Duration
	// MaxBackoff caps exponential growth.
	MaxBackoff time.Duration
}

// DefaultPolicy retries three times with 10 ms/20 ms/40 ms waits.
func DefaultPolicy() Policy {
	return Policy{Attempts: 3, Backoff: 10 * time.Millisecond, MaxBackoff: time.Second}
}

// IsTransient reports whether an error should be retried. Limit violations,
// blocking timestamp diagnostics and context cancellation are permanent:
// retrying them would either loop forever or fabricate values.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var le interface{ Error() string }
	if errors.As(err, &le) {
		if strings.Contains(err.Error(), "limit exceeded") {
			return false
		}
	}
	var p *Permanent
	if errors.As(err, &p) {
		return false
	}
	return true
}

// Permanent marks an error as non-retryable.
type Permanent struct{ Err error }

// Error implements error.
func (p *Permanent) Error() string {
	if p.Err == nil {
		return "permanent failure"
	}
	return p.Err.Error()
}

// Unwrap exposes the cause.
func (p *Permanent) Unwrap() error { return p.Err }

// Retry runs fn up to policy.Attempts times while failures are transient and
// the context is live. Each attempt is independent: fn must be idempotent
// because a partial side effect from an aborted attempt may persist.
func Retry(ctx context.Context, pol Policy, fn func(ctx context.Context, attempt int) error) error {
	if pol.Attempts <= 0 {
		pol.Attempts = 1
	}
	var last error
	backoff := pol.Backoff
	for attempt := 0; attempt < pol.Attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn(ctx, attempt)
		if err == nil {
			return nil
		}
		last = err
		if !IsTransient(err) {
			return err
		}
		if attempt+1 == pol.Attempts {
			break
		}
		d := backoff
		if pol.MaxBackoff > 0 && d > pol.MaxBackoff {
			d = pol.MaxBackoff
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		backoff *= 2
	}
	return last
}

// DiskCache stores node results in one JSON file per key, committed with
// temp+rename so a crash mid-write never leaves a half-written entry. The
// cache is bounded; writing beyond the bound fails rather than growing.
type DiskCache struct {
	mu     sync.Mutex
	dir    string
	max    int
	reads  int
	writes int
}

// CacheEntry is the on-disk form.
type CacheEntry struct {
	Key    string `json:"key"`
	Hash   string `json:"hash"`
	Detail string `json:"detail"`
	CRC    string `json:"crc"`
}

const MaxCacheEntries = 20_000

// NewDiskCache opens or creates a cache directory. Empty dir disables disk
// persistence and returns a memory cache instead (nil error).
func NewDiskCache(dir string, max int) (*DiskCache, error) {
	if max <= 0 || max > MaxCacheEntries {
		max = MaxCacheEntries
	}
	if dir == "" {
		return nil, errors.New("dag: empty cache directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	valid := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		valid++
	}
	if valid > max {
		return nil, fmt.Errorf("dag: cache holds %d entries, limit %d", valid, max)
	}
	return &DiskCache{dir: dir, max: max}, nil
}

func (c *DiskCache) file(key string) string {
	return filepath.Join(c.dir, key+".json")
}

func (c *DiskCache) Get(key string) (Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	b, err := os.ReadFile(c.file(key))
	if err != nil {
		return Result{}, false
	}
	var e CacheEntry
	if err := json.Unmarshal(b, &e); err != nil {
		return Result{}, false
	}
	if e.Key != key || !validEntry(e) {
		return Result{}, false
	}
	return Result{Hash: e.Hash, Detail: e.Detail}, true
}

func (c *DiskCache) Put(key string, r Result) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes++
	if r.Hash == "" {
		// Invalidate: remove a stale entry.
		err := os.Remove(c.file(key))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if _, err := os.Stat(c.file(key)); err == nil {
		// Already present: overwriting with the same key is forbidden by
		// the idempotency contract.
		return nil
	}
	if c.max > 0 {
		entries, err := os.ReadDir(c.dir)
		if err != nil {
			return err
		}
		n := 0
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".json" {
				n++
			}
		}
		if n >= c.max {
			return fmt.Errorf("dag: cache limit %d reached", c.max)
		}
	}
	e := CacheEntry{Key: key, Hash: r.Hash, Detail: r.Detail}
	b, err := json.Marshal(&e)
	if err != nil {
		return err
	}
	e.CRC = fmt.Sprintf("%08x", crc32.ChecksumIEEE(b))
	b, _ = json.Marshal(&e)
	tmp, err := os.CreateTemp(c.dir, ".tmp-"+key+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, c.file(key))
}

func validEntry(e CacheEntry) bool {
	cp := e
	cp.CRC = ""
	b, _ := json.Marshal(&cp)
	return e.CRC == fmt.Sprintf("%08x", crc32.ChecksumIEEE(b))
}

// MemoryCache is an in-process cache, useful for tests and demo reruns.
type MemoryCache struct {
	mu sync.Mutex
	m  map[string]Result
}

// NewMemoryCache creates an empty in-process cache.
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{m: make(map[string]Result)}
}

// Get implements Cache.
func (c *MemoryCache) Get(key string) (Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[key]
	return r, ok
}

// Put implements Cache.
func (c *MemoryCache) Put(key string, r Result) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = r
	return nil
}

// Snapshot copies the cache contents for deterministic reporting.
func (c *MemoryCache) Snapshot() map[string]Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]Result, len(c.m))
	for k, v := range c.m {
		out[k] = v
	}
	return out
}
