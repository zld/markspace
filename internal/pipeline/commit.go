package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"os"

	"mediamerge/internal/fault"
	"mediamerge/internal/merge"
	"mediamerge/internal/report"
	"mediamerge/internal/track"
	"mediamerge/internal/txn"
)

// segmentCount is the fixed number of output segments (8 cues / 4 segments).
const segmentCount = 4

// cueSegment assigns cues round-robin into the fixed segment count using a
// stable two-cues-per-segment layout.
func cueSegment(index, total int) int {
	if total == 0 {
		return 1
	}
	per := total / segmentCount
	if per == 0 {
		per = 1
	}
	s := index/per + 1
	if s > segmentCount {
		s = segmentCount
	}
	return s
}

// segmentPayload renders the deterministic bytes for one segment from its
// cues. Identical cue sets always produce identical payloads, which keeps the
// per-segment hashes reproducible across runs.
func segmentPayload(cues []*track.Cue) []byte {
	var b []byte
	for _, c := range cues {
		b = append(b, []byte(c.ID+"|"+c.Text+"\n")...)
	}
	if len(b) == 0 {
		// Even an empty segment has a stable, non-random payload.
		b = []byte("empty\n")
	}
	return b
}

func commitSegments(cfg Config, m *report.Metrics, res *merge.Result) error {
	j, err := txn.Open(cfg.OutDir)
	if err != nil {
		return err
	}
	var allCues []*track.Cue
	for _, tl := range res.Timelines {
		for i := range tl.Cues {
			c := tl.Cues[i]
			allCues = append(allCues, &c)
		}
	}
	buckets := make([][]*track.Cue, segmentCount)
	for i, c := range allCues {
		idx := cueSegment(i, len(allCues)) - 1
		buckets[idx] = append(buckets[idx], c)
	}
	var segs []txn.Segment
	for i := 0; i < segmentCount; i++ {
		payload := segmentPayload(buckets[i])
		path := j.StagePath(i + 1)
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			return fault.New(fault.StageTxn, fault.KindIO, path, err.Error())
		}
		seg, err := j.StageSegment(i + 1)
		if err != nil {
			return err
		}
		segs = append(segs, *seg)
		// Simulate an interrupt after the requested number of staged steps:
		// the journal persists but no commit record is written.
		if cfg.InterruptAfter > 0 && i+1 == cfg.InterruptAfter {
			m.MarkInterrupted()
			return ErrInterrupted
		}
	}
	manifest, err := j.Commit(segs, len(allCues))
	if err != nil {
		return err
	}
	m.FromManifest(manifest, 0)
	return nil
}

func rollbackReason(outDir string) string {
	j, err := txn.Open(outDir)
	if err != nil {
		return ""
	}
	return j.RollbackReason()
}

// hashSegment is a small helper retained for report consistency checks.
func hashSegment(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
