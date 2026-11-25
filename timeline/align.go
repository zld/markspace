package timeline

import (
	"fmt"
	"sort"
	"sync"

	"mediapack/model"
)

// SyncPair is one matched audio/video timestamp comparison.
type SyncPair struct {
	Index       int   `json:"index"`
	VideoNanos  int64 `json:"video_nanos"`
	AudioNanos  int64 `json:"audio_nanos"`
	OffsetNanos int64 `json:"offset_nanos"`
}

// SyncReport summarizes cross-track timestamp agreement.
type SyncReport struct {
	Matched        int        `json:"matched"`
	MaxOffsetNanos int64      `json:"max_av_offset_nanos"`
	Pairs          []SyncPair `json:"pairs,omitempty"`
}

// MaxAVOffset pairs points by index and returns the largest absolute PTS delta.
func MaxAVOffset(video, audio []int64) SyncReport {
	n := len(video)
	if len(audio) < n {
		n = len(audio)
	}
	report := SyncReport{Matched: n}
	for i := 0; i < n; i++ {
		delta := video[i] - audio[i]
		if delta < 0 {
			delta = -delta
		}
		if delta > report.MaxOffsetNanos {
			report.MaxOffsetNanos = delta
		}
		if i < 5 {
			report.Pairs = append(report.Pairs, SyncPair{
				Index: i, VideoNanos: video[i], AudioNanos: audio[i], OffsetNanos: delta,
			})
		}
	}
	return report
}

// Dedup removes repeated events with the same stable identity. Identity should
// include source location or a caller-provided key for timestamp-only repeats.
type Dedup struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// NewDedup creates an event deduplicator.
func NewDedup() *Dedup {
	return &Dedup{seen: make(map[string]struct{})}
}

// EventKey builds a stable identity for an event.
func EventKey(e model.Event) string {
	if e.Key != "" {
		return string(e.Track) + "|" + string(e.Kind) + "|" + e.Key
	}
	return fmt.Sprintf("%s|%s|%s|%d|%d|%d", e.Track, e.Kind, e.Source, e.Offset, e.StartNanos, e.EndNanos)
}

// Add returns false when the event is a repeat.
func (d *Dedup) Add(e model.Event) bool {
	key := EventKey(e)
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[key]; ok {
		return false
	}
	d.seen[key] = struct{}{}
	return true
}

// KeyframeGate records which keyframe boundaries have been safely observed.
type KeyframeGate struct {
	mu    sync.Mutex
	ready map[model.TrackID]map[int64]struct{}
}

// NewKeyframeGate creates an empty gate.
func NewKeyframeGate() *KeyframeGate {
	return &KeyframeGate{ready: make(map[model.TrackID]map[int64]struct{})}
}

// MarkReady marks an exact common-timeline keyframe boundary as available.
func (g *KeyframeGate) MarkReady(track model.TrackID, boundaryNanos int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ready[track] == nil {
		g.ready[track] = make(map[int64]struct{})
	}
	g.ready[track][boundaryNanos] = struct{}{}
}

// IsReady reports whether an exact keyframe boundary is available.
func (g *KeyframeGate) IsReady(track model.TrackID, boundaryNanos int64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.ready[track][boundaryNanos]
	return ok
}

// MissingBoundary describes a dependency that blocks sealing.
type MissingBoundary struct {
	Track    model.TrackID `json:"track"`
	Boundary int64         `json:"boundary_nanos"`
}

// SealCheck reports whether every keyframe dependency is ready at boundary.
// Deps are usually obtained from Graph.Node(waiter).DependsOn and filtered to
// video tracks by the caller.
func (g *KeyframeGate) SealCheck(boundaryNanos int64, deps ...model.TrackID) (bool, []MissingBoundary) {
	var missing []MissingBoundary
	for _, dep := range deps {
		if !g.IsReady(dep, boundaryNanos) {
			missing = append(missing, MissingBoundary{Track: dep, Boundary: boundaryNanos})
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].Track < missing[j].Track })
	return len(missing) == 0, missing
}

// RequireSeal is the error-returning form used by the pipeline.
func (g *KeyframeGate) RequireSeal(waiter model.TrackID, boundaryNanos int64, deps ...model.TrackID) error {
	ok, missing := g.SealCheck(boundaryNanos, deps...)
	if ok {
		return nil
	}
	return model.NewError(waiter, "", boundaryNanos, model.CategoryNotReady,
		fmt.Sprintf("keyframe boundary %d not ready for dependency %s", boundaryNanos, missing[0].Track), nil)
}

// SortedNanos is a test/helper that sorts int64 timestamps in place.
func SortedNanos(in []int64) []int64 {
	out := append([]int64(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
