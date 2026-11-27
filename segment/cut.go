// Package segment cuts inputs at common keyframe-aligned time points and
// tracks processing state so interrupted runs resume safely.
package segment

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"continuity/media"
	"continuity/planner"
	"continuity/worker"
)

// Segment is one cut of one representation.
type Segment struct {
	Rep        int    `json:"rep"`
	Index      int    `json:"index"`
	StartNanos int64  `json:"start_nanos"`
	EndNanos   int64  `json:"end_nanos"`
	SHA256     string `json:"sha256"`
}

// FileName is the on-disk name of the segment artifact.
func (s *Segment) FileName() string {
	return fmt.Sprintf("seg-r%d-%04d.bin", s.Rep, s.Index)
}

// Result summarizes one cut run.
type Result struct {
	Segments   []*Segment
	Reused     int
	Recomputed int
	HitRate    float64
}

// Cut verifies that every interior cut point lands on a keyframe in
// every representation, then produces segment artifacts with bounded
// concurrency. A keyframe miss in any representation fails the whole
// run; no partial manifest is published.
func Cut(ctx context.Context, inputs []*media.Container, plan *planner.Plan,
	outDir string, pool *worker.Pool) (*Result, error) {

	indexes := make([]*media.SampleIndex, len(inputs))
	for i, in := range inputs {
		ix, err := media.BuildIndex(in.VideoTrack())
		if err != nil {
			return nil, err
		}
		indexes[i] = ix
	}

	// Keyframe alignment check on interior boundaries.
	hits, total := 0, 0
	for _, cut := range plan.Cuts[1 : len(plan.Cuts)-1] {
		for rep, ix := range indexes {
			total++
			if ix.HasKeyframeAt(cut) {
				hits++
			} else {
				return nil, &media.Error{Kind: media.KindKeyframeMiss,
					Op: "segment.cut", TrackID: rep + 1,
					Msg: fmt.Sprintf("representation %d has no keyframe at cut %d ns", rep, cut)}
			}
		}
	}
	res := &Result{HitRate: 1.0}
	if total > 0 {
		res.HitRate = float64(hits) / float64(total)
	}

	state, err := LoadState(outDir)
	if err != nil {
		return nil, err
	}
	if !state.Matches(inputs, plan.PlanHash) {
		state = NewState(inputs, plan.PlanHash)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}

	// Plan all segments, then execute with bounded concurrency.
	type job struct {
		seg *Segment
		rep int
	}
	var jobs []job
	for rep := range inputs {
		for i := 0; i+1 < len(plan.Cuts); i++ {
			jobs = append(jobs, job{&Segment{
				Rep: rep, Index: i,
				StartNanos: plan.Cuts[i], EndNanos: plan.Cuts[i+1],
			}, rep})
		}
	}

	tasks := make([]worker.Task, len(jobs))
	var mu sync.Mutex // guards state.Segments and the reuse counters
	for j, jb := range jobs {
		jb := jb
		tasks[j] = worker.Task{
			ID: fmt.Sprintf("r%d/s%d", jb.rep, jb.seg.Index),
			Run: func(ctx context.Context) error {
				path := filepath.Join(outDir, jb.seg.FileName())
				key := stateKey(jb.seg)
				mu.Lock()
				recorded, ok := state.Segments[key]
				mu.Unlock()
				if ok {
					if h, err := hashFile(path); err == nil && h == recorded {
						jb.seg.SHA256 = recorded
						mu.Lock()
						res.Reused++
						mu.Unlock()
						return nil
					}
				}
				data, err := buildSegment(inputs[jb.rep], jb.seg)
				if err != nil {
					return err
				}
				sum := sha256.Sum256(data)
				jb.seg.SHA256 = hex.EncodeToString(sum[:])
				if err := os.WriteFile(path, data, 0o644); err != nil {
					return err
				}
				mu.Lock()
				state.Segments[key] = jb.seg.SHA256
				res.Recomputed++
				mu.Unlock()
				return nil
			},
		}
	}
	if err := pool.Run(ctx, tasks); err != nil {
		return nil, err
	}
	for _, jb := range jobs {
		res.Segments = append(res.Segments, jb.seg)
	}
	if err := state.Save(outDir); err != nil {
		return nil, err
	}
	return res, nil
}

// buildSegment produces the deterministic byte content of one segment:
// a header followed by the source sample bytes of every track covering
// [start, end).
func buildSegment(in *media.Container, seg *Segment) ([]byte, error) {
	f, err := os.Open(in.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := []byte("CONTSEG1")
	var hdr [8]byte
	binary.BigEndian.PutUint64(hdr[:], uint64(seg.StartNanos))
	out = append(out, hdr[:]...)
	binary.BigEndian.PutUint64(hdr[:], uint64(seg.EndNanos))
	out = append(out, hdr[:]...)

	for _, t := range in.Tracks {
		ix, err := media.BuildIndex(t)
		if err != nil {
			return nil, err
		}
		from, to := ix.SampleRange(seg.StartNanos, seg.EndNanos)
		if len(t.ChunkOffsets) == 0 || to > len(t.SampleSizes) {
			return nil, &media.Error{Kind: media.KindTruncatedBox,
				Op: "segment.build", TrackID: t.TrackID,
				Msg: "sample table shorter than sample index"}
		}
		off := int64(t.ChunkOffsets[0])
		for _, s := range t.SampleSizes[:from] {
			off += int64(s)
		}
		var n int64
		for _, s := range t.SampleSizes[from:to] {
			n += int64(s)
		}
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, off); err != nil && n > 0 {
			return nil, &media.Error{Kind: media.KindTruncatedBox,
				Op: "segment.build", TrackID: t.TrackID,
				Offset: off, HasOffset: true, Msg: "sample data unreadable"}
		}
		out = append(out, buf...)
	}
	return out, nil
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
