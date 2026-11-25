package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mediad/internal/dag"
	"mediad/internal/model"
	"mediad/internal/segment"
)

// repID returns the stable representation identifier for tier i.
func repID(i int) string {
	if i == 0 {
		return "video-high"
	}
	return "video-low"
}

// routePacket feeds one video packet to its cutter and, when a segment
// closes, immediately muxes and commits it. At most one segment's samples
// per representation are retained at any time.
func routePacket(
	r *repState,
	pkt *model.Packet,
	base model.TimeBase,
	align *segment.Align,
	muxer *segment.Muxer,
	cp *dag.Checkpoint,
) error {
	closed, err := r.cutter.AddPacket(pkt)
	if err != nil {
		return err
	}
	r.samples = append(r.samples, pkt.Raw[:])

	// Record the observed boundary for cross-representation alignment.
	segIdx := base.SegmentIndex(r.timeline.MediaTicks())
	align.Observe(segIdx, r.id, r.timeline.MediaTicks())

	if closed != nil {
		if err := finalizeSegment(r, closed, muxer, cp, base); err != nil {
			return err
		}
		r.samples = nil
	}
	return nil
}

// finalizeSegment writes one segment through the temp-file muxer and commits
// it to the checkpoint ledger. The ledger makes this idempotent: a resumed
// run that reaches an already-committed key reuses the record instead of
// writing a second file.
func finalizeSegment(
	r *repState,
	d *segment.Descriptor,
	muxer *segment.Muxer,
	cp *dag.Checkpoint,
	base model.TimeBase,
) error {
	key := dag.TaskKey(r.id, d.Index)
	if cp.IsCommitted(key) {
		// Replayed adopted segment: the file and ledger record already exist.
		// Drop the re-accumulated samples so memory stays bounded and copy
		// the authoritative digest/size from the ledger onto the descriptor.
		r.samples = nil
		if commit, ok := ledgerLookup(cp, key); ok {
			d.SHA256 = commit.SHA256
			d.ByteSize = commit.ByteSize
		}
		return nil
	}
	decodeTime := uint64(d.StartTicks)
	name, size, err := muxer.WriteSegment(r.id, d.Index, decodeTime, r.samples)
	if err != nil {
		return err
	}
	r.samples = nil
	d.ByteSize = size
	if _, _, err := cp.CommitSegment(dag.SegmentCommit{
		Key: key, File: name, SHA256: d.SHA256,
		ByteSize: size, Index: d.Index, Rep: r.id,
	}); err != nil {
		return err
	}
	return cp.SetInputOffset("segment:"+r.id, d.EndOffset)
}

// ledgerLookup returns the committed record for a stable task key.
func ledgerLookup(cp *dag.Checkpoint, key string) (dag.SegmentCommit, bool) {
	for _, c := range cp.Commits() {
		if c.Key == key {
			return c, true
		}
	}
	return dag.SegmentCommit{}, false
}

// allRepsAtBoundary reports whether every representation has committed N
// segments, i.e. the next packet for any rep would open index >= N.
func allRepsAtBoundary(reps []*repState, n int) bool {
	for _, r := range reps {
		if r.cutter.NextIndex(r.timeline.MediaTicks()) < n {
			return false
		}
	}
	return true
}

// interruptTarget parses "segment:N" into N, the number of segments that
// must be committed before the run pauses. A non-positive spec disables it.
func interruptTarget(spec string) (int, bool, error) {
	if spec == "" {
		return 0, false, nil
	}
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 || parts[0] != "segment" {
		return 0, false, fmt.Errorf("engine: invalid interrupt spec %q", spec)
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 0 {
		return 0, false, fmt.Errorf("engine: invalid interrupt spec %q", spec)
	}
	return n, true, nil
}

// classifyFailure maps internal failures onto the CLI exit contract: media
// and dependency failures stay as errors the caller turns into exit code 1.
func classifyFailure(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return err
}

// firstResumableSegment is the smallest segment index present in the ledger,
// reported as resumed_from_segment after a successful resume.
func firstResumableSegment(cp *dag.Checkpoint) int {
	commits := cp.Commits()
	if len(commits) == 0 {
		return 0
	}
	min := commits[0].Index
	for _, c := range commits[1:] {
		if c.Index < min {
			min = c.Index
		}
	}
	return min
}

// atomicWrite writes data to a sibling temp file and renames it into place.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".tmp-"+filepath.Base(path))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
