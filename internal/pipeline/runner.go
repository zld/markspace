// Package pipeline provides checkpointed, idempotent execution over the media
// kernel modules. It owns the content-addressed cache and guarantees that a
// failed or rejected segment never contaminates committed output.
package pipeline

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
)

// SegmentWork is the handler for one segment index. It returns the segment's
// digest-bearing content and must not have side effects on failure.
type SegmentWork func(index int) (content []byte, digest string, err error)

// RejectError marks a segment the aligner refused. Rejections are resumable:
// they do not fail the run but are recorded and left pending.
type RejectError struct{ Reason string }

func (e *RejectError) Error() string { return "pipeline: segment rejected: " + e.Reason }

// RunConfig controls execution.
type RunConfig struct {
	TotalSegments int
	// CommitEvery persists a checkpoint after this many newly completed
	// segments. Zero commits only at the end.
	CommitEvery int
	// Recompute is called (if non-nil) to verify already-completed segments
	// before resuming. If it returns a digest that differs, the run fails with
	// ErrCorruptCheckpoint rather than trusting the checkpoint.
	Recompute func(index int) (string, error)
	// Cache, when non-nil, is the content-addressed store consulted before
	// computing a pending segment. A hit is accepted only after the Cache has
	// re-verified the input provenance, config digest and content hash; a miss
	// (including a quarantined entry) falls back to normal computation.
	Cache *Cache
}

// RunResult summarizes a run.
type RunResult struct {
	Total       int            `json:"total"`
	Completed   int            `json:"completed"`
	Recomputed  int            `json:"recomputed"`
	Reused      int            `json:"reused"`
	CacheHits   int            `json:"cache_hits"`
	CacheMisses int            `json:"cache_misses"`
	Quarantined int            `json:"cache_quarantined"`
	Rejected    int            `json:"rejected"`
	Rejections  []Rejection    `json:"rejections,omitempty"`
	Digests     map[int]string `json:"-"`
}

// Runner executes segments with checkpointing and idempotent commits.
type Runner struct {
	CheckpointPath string
	cp             *Checkpoint
	mu             sync.Mutex
}

// NewRunner resumes from an existing checkpoint or starts a new one. If a
// checkpoint exists it is checked against the current input/config digest and
// its completed entries are re-verified with cfg.Recompute.
func NewRunner(path string, inputDigest, configDigest string, cfg RunConfig) (*Runner, error) {
	cp, err := LoadCheckpoint(path, inputDigest, configDigest)
	switch {
	case err == nil:
		if cfg.Recompute != nil {
			if err := cp.VerifyCompleted(cfg.Recompute); err != nil {
				return nil, err
			}
		}
	case isNotExist(err):
		cp = NewCheckpoint(inputDigest, configDigest)
	default:
		return nil, err
	}
	return &Runner{CheckpointPath: path, cp: cp}, nil
}

// SetParseOffset records the parser's progress in the checkpoint. It is only
// meaningful before the first save; later saves persist it unchanged.
func (r *Runner) SetParseOffset(offset int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cp.ParseOffset = offset
}

// Checkpoint returns the live checkpoint (read-only for callers).
func (r *Runner) Checkpoint() *Checkpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *r.cp
	clone.Completed = append([]CompletedSegment(nil), r.cp.Completed...)
	clone.Rejections = append([]Rejection(nil), r.cp.Rejections...)
	return &clone
}

// Run executes pending segments in ascending index order. alreadyDone and
// work are combined with any checkpoint state. A RejectError leaves the
// segment pending and records the reason; other errors abort without
// committing the failing segment.
func (r *Runner) Run(cfg RunConfig, work SegmentWork) (*RunResult, error) {
	if cfg.TotalSegments < 0 {
		return nil, fmt.Errorf("pipeline: negative segment count")
	}
	res := &RunResult{Total: cfg.TotalSegments, Digests: map[int]string{}}
	done := r.cp.CompletedSet()
	for i := range done {
		res.Digests[i] = done[i]
		res.Reused++
	}
	pending := r.cp.Pending(cfg.TotalSegments)
	res.Completed = len(r.cp.Completed)
	newlyDone := 0
	for _, index := range pending {
		// Content-addressed fast path: the Cache re-verifies input provenance,
		// config digest and content hash before reporting a hit carrying the
		// authoritative work digest, so the checkpoint entry matches a fresh
		// computation exactly.
		if digest, ok, quarantined := r.cacheLookup(cfg.Cache, index); quarantined {
			res.Quarantined++
		} else if ok {
			if err := r.cp.MarkCompleted(index, digest); err != nil {
				return res, err
			}
			res.Digests[index] = digest
			res.CacheHits++
			res.Completed++
			newlyDone++
			continue
		}
		content, digest, err := work(index)
		if err != nil {
			var rej *RejectError
			if asReject(err, &rej) {
				r.addRejection(index, rej.Reason)
				res.Rejected++
				continue
			}
			// Abort before committing this segment: partial output cannot leak.
			if saveErr := r.cp.Save(r.CheckpointPath); saveErr != nil {
				return res, fmt.Errorf("%v (also failed to save checkpoint: %v)", err, saveErr)
			}
			return res, err
		}
		if digest == "" {
			digest = DigestBytes(content)
		}
		if cfg.Cache != nil {
			// Store for future runs. A put failure does not lose the result: the
			// checkpoint still commits it and a later run recomputes.
			if putErr := cfg.Cache.PutWithDigest(SegmentCacheKey(r.cp.InputDigest, index), r.cp.ConfigDigest, content, digest); putErr != nil {
				return res, fmt.Errorf("pipeline: cache store segment %d: %w", index, putErr)
			}
		}
		res.CacheMisses++
		if err := r.cp.MarkCompleted(index, digest); err != nil {
			return res, err
		}
		res.Digests[index] = digest
		res.Recomputed++
		res.Completed++
		newlyDone++
		if cfg.CommitEvery > 0 && newlyDone >= cfg.CommitEvery {
			if err := r.cp.Save(r.CheckpointPath); err != nil {
				return res, err
			}
			newlyDone = 0
		}
	}
	if err := r.cp.Save(r.CheckpointPath); err != nil {
		return res, err
	}
	res.Rejections = append(res.Rejections, r.cp.Rejections...)
	return res, nil
}

func (r *Runner) addRejection(index int, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rej := range r.cp.Rejections {
		if rej.Index == index {
			return
		}
	}
	r.cp.Rejections = append(r.cp.Rejections, Rejection{Index: index, Reason: reason})
}

// SegmentCacheKey returns the content-address key for one segment of an input.
// It is exported so callers replaying previously delivered work can warm the
// cache with exactly the keys a later runner will look up.
func SegmentCacheKey(inputDigest string, index int) string {
	return DigestBytes([]byte(inputDigest + "|segment|" + strconv.Itoa(index)))
}

// cacheLookup consults the content-addressed store for one pending segment.
// ok means the stored content passed the Cache's input/config/content-hash
// verification; quarantined means a stale or tampered entry was set aside and
// the segment must be recomputed.
func (r *Runner) cacheLookup(c *Cache, index int) (digest string, ok, quarantined bool) {
	if c == nil {
		return "", false, false
	}
	hit, err := c.Get(SegmentCacheKey(r.cp.InputDigest, index), r.cp.ConfigDigest)
	if err != nil {
		return "", false, false
	}
	if hit.Quarantined {
		return "", false, true
	}
	if !hit.Found {
		return "", false, false
	}
	return hit.Digest, true, false
}

func asReject(err error, out **RejectError) bool {
	for err != nil {
		if re, ok := err.(*RejectError); ok {
			*out = re
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func isNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }
