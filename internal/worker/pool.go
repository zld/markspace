// Package worker runs the bounded processing pool. Only complete segments are
// submitted; progress is checkpointed atomically and each completed digest is
// recorded so a restart skips finished work and reproduces a full run exactly.
package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"tsindex/internal/digest"
	"tsindex/internal/index"
	"tsindex/internal/ingest"
)

// SegmentResult is the verified outcome of processing one segment.
type SegmentResult struct {
	SegmentID   int64
	StartOffset int64
	EndOffset   int64
	Packets     int
	Digests     digest.Digests
	Keyframes   []index.Keyframe
	Incomplete  bool
	// Change classifies this run's digest versus the checkpointed one. A
	// fresh segment with no prior entry is reported as ChangeContent.
	Change digest.ChangeKind
}

// Checkpoint is the on-disk resume state, written via temp-file + rename.
type Checkpoint struct {
	Version           int             `json:"version"`
	SourceName        string          `json:"source"`
	PacketsPerSegment int             `json:"packets_per_segment"`
	Completed         []CompletedItem `json:"completed"`
	// Interrupted marks a cooperative stop; a clean run leaves it false.
	Interrupted bool `json:"interrupted"`
}

// CompletedItem binds a finished segment to its verified hashes.
type CompletedItem struct {
	SegmentID   int64           `json:"segment_id"`
	StartOffset int64           `json:"start_offset"`
	EndOffset   int64           `json:"end_offset"`
	RawHash     [digest.Size]byte `json:"raw_hash"`
	ContentHash [digest.Size]byte `json:"content_hash"`
	Sidecar     string          `json:"sidecar"`
}

const checkpointVersion = 1

// ErrInterrupt marks the cooperative stop requested after N segments. It is
// distinguishable from a real failure so the CLI can return exit code 75.
var ErrInterrupt = errors.New("worker: cooperative interrupt")

// Pool processes segments with a fixed worker count. It is single-use: create
// one pool per run (or per resume).
type Pool struct {
	workers           int
	dir               string
	sourceName        string
	packetsPerSegment int
	epoch             *ingest.MappingEpoch
	chkPath           string
	mu                sync.Mutex
	done              map[int64]CompletedItem
	processed         int
	inFlight          int32 // atomic: currently computing segments
	maxFlight         int32 // atomic: high-water mark of inFlight
}

// NewPool builds a pool. Already-completed items from a prior checkpoint make
// the pool skip those segment IDs without recomputation. The interruptAfter
// argument is accepted for API stability but interruption is driven at asset
// granularity by the caller; pass zero for normal segment processing.
func NewPool(workers int, dir, sourceName string, epoch *ingest.MappingEpoch,
	prior *Checkpoint, interruptAfter int) *Pool {
	_ = interruptAfter
	p := &Pool{
		workers:    workers,
		dir:        dir,
		sourceName: sourceName,
		epoch:      epoch,
		chkPath:    filepath.Join(dir, checkpointName(sourceName)),
		done:       map[int64]CompletedItem{},
	}
	if prior != nil {
		for _, item := range prior.Completed {
			p.done[item.SegmentID] = item
		}
	}
	return p
}

// SetEpoch rebinds the mapping epoch. The pool is created before the first
// PAT arrives; once PSI has been parsed the driver installs the live epoch so
// keyframe scanning uses the current program/track mapping.
func (p *Pool) SetEpoch(epoch *ingest.MappingEpoch) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.epoch = epoch
}

// SetSegmentSize records the configured packets-per-segment so the checkpoint
// captures the segmentation used; a resume with a different size is detectable.
func (p *Pool) SetSegmentSize(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.packetsPerSegment = n
}

// IsDone reports whether a segment was completed and verified in a prior run.
func (p *Pool) IsDone(segmentID int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.done[segmentID]
	return ok
}

// Process handles one complete segment and writes its sidecar. Incomplete
// segments never reach here: the ingest layer withholds partial tails.
func (p *Pool) Process(seg *ingest.Segment) (SegmentResult, error) {
	if p.IsDone(seg.ID) {
		// Reused asset: return the stored identity, no recomputation.
		item := p.done[seg.ID]
		return SegmentResult{
			SegmentID: item.SegmentID, StartOffset: item.StartOffset,
			EndOffset: item.EndOffset, Digests: digest.Digests{
				Raw: item.RawHash, Content: item.ContentHash},
		}, nil
	}

	result, rec, err := p.computeSegment(seg, nil)
	if err != nil {
		return SegmentResult{}, err
	}
	d := result.Digests
	sidecar, err := index.WriteSidecar(p.dir, p.chkSource(seg.ID), rec)
	if err != nil {
		return SegmentResult{}, err
	}
	p.mu.Lock()
	p.done[seg.ID] = CompletedItem{
		SegmentID: seg.ID, StartOffset: seg.StartOffset, EndOffset: seg.EndOffset,
		RawHash: d.Raw, ContentHash: d.Content, Sidecar: sidecar,
	}
	p.processed++
	p.mu.Unlock()

	// The checkpoint is written after every completed segment so a crash at
	// any boundary can resume; cooperative interruption is driven at asset
	// granularity by the caller, not inside the segment processor.
	if err := p.persist(false); err != nil {
		return result, err
	}
	return result, nil
}

// computeSegment performs the CPU-bound, side-effect-free work: two-tier
// hashing and keyframe scanning. It touches no shared pool state and no disk,
// which is what makes it safe to fan out across the bounded worker pool. When
// prior is the checkpointed digest for this segment, the result's Change field
// distinguishes an unchanged segment, a remux and a real content edit.
func (p *Pool) computeSegment(seg *ingest.Segment, prior *digest.Digests) (SegmentResult, index.SegmentRecord, error) {
	hasher := digest.NewHasher()
	scanner := index.NewScanner(p.epoch)
	for i := range seg.Records {
		rec := &seg.Records[i]
		hasher.AddPacket(rec.Raw[:], rec.Payload())
		scanner.Feed(rec)
	}
	d := hasher.Sum()
	change := digest.ChangeContent
	if prior != nil {
		change = d.Classify(*prior)
	}
	kfs := scanner.Frames()
	incomplete := false
	for _, kf := range kfs {
		if !kf.Complete {
			incomplete = true
			break
		}
	}
	rec := index.SegmentRecord{
		SegmentID: seg.ID, StartOffset: seg.StartOffset, EndOffset: seg.EndOffset,
		Packets: len(seg.Records), RawHash: d.Raw, ContentHash: d.Content,
		Keyframes: kfs, Incomplete: incomplete,
	}
	result := SegmentResult{
		SegmentID: seg.ID, StartOffset: seg.StartOffset, EndOffset: seg.EndOffset,
		Packets: len(seg.Records), Digests: d, Keyframes: kfs, Incomplete: incomplete,
		Change: change,
	}
	return result, rec, nil
}

// ProcessBatch runs complete segments through a bounded worker pool. At most
// p.workers segments compute concurrently; results are returned in input order
// regardless of completion order so downstream indexing stays deterministic.
// Already-checkpointed segments are skipped (their stored identity is reused)
// and never recomputed.
func (p *Pool) ProcessBatch(segs []*ingest.Segment) ([]SegmentResult, error) {
	results := make([]SegmentResult, len(segs))
	sem := make(chan struct{}, p.workers)
	var wg sync.WaitGroup
	var errOnce sync.Once
	var firstErr error
	setErr := func(e error) { errOnce.Do(func() { firstErr = e }) }

	for i, seg := range segs {
		if p.IsDone(seg.ID) {
			p.mu.Lock()
			item := p.done[seg.ID]
			p.mu.Unlock()
			results[i] = SegmentResult{
				SegmentID: item.SegmentID, StartOffset: item.StartOffset,
				EndOffset: item.EndOffset, Digests: digest.Digests{
					Raw: item.RawHash, Content: item.ContentHash},
			}
			continue
		}
		wg.Add(1)
		sem <- struct{}{} // acquire a worker slot, bounding concurrency
		go func(idx int, s *ingest.Segment) {
			defer wg.Done()
			defer func() { <-sem }()
			cur := atomic.AddInt32(&p.inFlight, 1)
			for {
				peak := atomic.LoadInt32(&p.maxFlight)
				if cur <= peak || atomic.CompareAndSwapInt32(&p.maxFlight, peak, cur) {
					break
				}
			}
			defer atomic.AddInt32(&p.inFlight, -1)
			result, rec, cerr := p.computeSegment(s, nil)
			if cerr != nil {
				setErr(cerr)
				return
			}
			sidecar, werr := index.WriteSidecar(p.dir, p.chkSource(s.ID), rec)
			if werr != nil {
				setErr(werr)
				return
			}
			p.mu.Lock()
			p.done[s.ID] = CompletedItem{
				SegmentID: s.ID, StartOffset: s.StartOffset, EndOffset: s.EndOffset,
				RawHash: rec.RawHash, ContentHash: rec.ContentHash, Sidecar: sidecar,
			}
			p.processed++
			p.mu.Unlock()
			results[idx] = result
		}(i, seg)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := p.persist(false); err != nil {
		return nil, err
	}
	return results, nil
}

// Workers returns the pool's bounded concurrency level.
func (p *Pool) Workers() int { return p.workers }

// MaxInFlight returns the largest number of segments that computed
// concurrently during the last ProcessBatch call.
func (p *Pool) MaxInFlight() int { return int(atomic.LoadInt32(&p.maxFlight)) }

// chkSource gives each segment a per-asset sidecar base name so distinct
// assets never overwrite one another's index.
func (p *Pool) chkSource(segmentID int64) string {
	return fmt.Sprintf("%s-segment-%06d.ts", p.sourceName, segmentID)
}

// persist atomically writes the checkpoint. On a clean finish interrupted is
// false; cooperative stops set it true.
func (p *Pool) persist(interrupted bool) error {
	p.mu.Lock()
	chk := Checkpoint{
		Version: checkpointVersion, SourceName: p.sourceName,
		PacketsPerSegment: p.packetsPerSegment, Interrupted: interrupted,
	}
	ids := make([]int64, 0, len(p.done))
	for id := range p.done {
		ids = append(ids, id)
	}
	// Stable order makes checkpoint diffs and resume deterministic.
	sortIDs(ids)
	for _, id := range ids {
		chk.Completed = append(chk.Completed, p.done[id])
	}
	p.mu.Unlock()

	data, err := json.MarshalIndent(&chk, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(p.dir, ".chk-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, p.chkPath)
}

// Finalize writes the clean (non-interrupted) checkpoint after all segments.
func (p *Pool) Finalize() error { return p.persist(false) }

// Interrupt writes the checkpoint flagged as interrupted.
func (p *Pool) Interrupt() error { return p.persist(true) }

// CompletedCount returns how many segments are currently marked done.
func (p *Pool) CompletedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.done)
}

// CheckpointPath exposes the atomic checkpoint location.
func (p *Pool) CheckpointPath() string { return p.chkPath }

func checkpointName(sourceName string) string {
	return "." + sourceName + ".chk.json"
}

func sortIDs(ids []int64) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j-1] > ids[j]; j-- {
			ids[j-1], ids[j] = ids[j], ids[j-1]
		}
	}
}

// LoadCheckpoint reads and validates a checkpoint, returning nil if absent.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var chk Checkpoint
	if err := json.Unmarshal(data, &chk); err != nil {
		return nil, fmt.Errorf("corrupt checkpoint: %w", err)
	}
	if chk.Version != checkpointVersion {
		return nil, fmt.Errorf("checkpoint version %d unsupported", chk.Version)
	}
	return &chk, nil
}
