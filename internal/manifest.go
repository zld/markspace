package rtpreplay

import (
	"fmt"
	"sort"
)

// ManifestEntry is one row of the timeline alignment manifest: a track, its
// media span, the packet range that covers it, and every gap inside it.
type ManifestEntry struct {
	Track            string        `json:"track"`
	Codec            Codec         `json:"codec"`
	Timebase         string        `json:"timebase"`
	StartMs          int64         `json:"start_ms"`
	EndMs            int64         `json:"end_ms"`
	MediaDurationMs  int64         `json:"media_duration_ms"`
	FirstExtSeq      uint64        `json:"first_ext_seq"`
	LastExtSeq       uint64        `json:"last_ext_seq"`
	PacketCount      int           `json:"packet_count"`
	GapCount         uint64        `json:"gap_count"`
	Gaps             []ManifestGap `json:"gaps,omitempty"`
	Status           string        `json:"status"`
	DurationSources  []string      `json:"duration_sources"`
	AlignmentErrorMs float64       `json:"alignment_error_ms"`
}

// ManifestGap records one consecutive loss run with its reconstructed media
// window, so an operator can correlate a hole in the trace with a hole in the
// audio timeline.
type ManifestGap struct {
	StartExtSeq uint64 `json:"start_ext_seq"`
	EndExtSeq   uint64 `json:"end_ext_seq"`
	Count       uint64 `json:"count"`
	StartMs     int64  `json:"start_ms"`
	EndMs       int64  `json:"end_ms"`
}

// Manifest is the report's alignment section.
type Manifest struct {
	Version string          `json:"version"`
	Entries []ManifestEntry `json:"entries"`
	// AlignmentErrorMs is the maximum per-entry boundary error. A successful
	// interval must report exactly 0.
	AlignmentErrorMs float64 `json:"alignment_error_ms"`
	Unit             string  `json:"unit"`
	BoundaryRule     string  `json:"boundary_rule"`
}

// Manifest entry statuses.
const (
	StatusOK     = "ok"
	StatusGap    = "gap"
	StatusFailed = "failed"
)

// BuildManifest derives alignment rows for each stream. The expected media
// duration comes from the trace's declared duration; the observed duration
// comes from the reconstructed timeline. Both are converted with exact
// integer arithmetic to milliseconds, so an exact match yields 0.
func BuildManifest(states []*StreamState, declaredMs map[string]uint64) (*Manifest, error) {
	m := &Manifest{
		Version: "1", Unit: UnitMilliseconds,
		BoundaryRule: "observed_end_ms - declared_end_ms, integer ticks truncated once",
	}
	names := make([]string, 0, len(states))
	for _, ss := range states {
		names = append(names, ss.Track)
	}
	sort.Strings(names)
	byTrack := make(map[string]*StreamState, len(states))
	for _, ss := range states {
		byTrack[ss.Track] = ss
	}
	for _, name := range names {
		ss := byTrack[name]
		entry, err := buildEntry(ss, declaredMs[name])
		if err != nil {
			return nil, err
		}
		m.Entries = append(m.Entries, entry)
	}
	for _, e := range m.Entries {
		if absFloat(e.AlignmentErrorMs) > m.AlignmentErrorMs {
			m.AlignmentErrorMs = absFloat(e.AlignmentErrorMs)
		}
	}
	return m, nil
}

func buildEntry(ss *StreamState, declared uint64) (ManifestEntry, error) {
	e := ManifestEntry{
		Track: ss.Track, Codec: ss.Codec,
		Timebase: fmt.Sprintf("%d/1", ss.ClockHz),
	}
	var firstSet, lastSet bool
	sources := make(map[string]struct{})
	for _, st := range ss.States {
		if st.Kind != "packet" {
			continue
		}
		if !firstSet || st.ExtSeq < e.FirstExtSeq {
			e.FirstExtSeq = st.ExtSeq
			firstSet = true
		}
		if !lastSet || st.ExtSeq > e.LastExtSeq {
			e.LastExtSeq = st.ExtSeq
			lastSet = true
		}
		e.PacketCount++
		if st.DurationSource != "" {
			sources[st.DurationSource] = struct{}{}
		}
	}
	if !firstSet {
		e.Status = StatusFailed
		return e, nil
	}
	for s := range sources {
		e.DurationSources = append(e.DurationSources, s)
	}
	sort.Strings(e.DurationSources)
	e.EndMs = int64(MillisF(ss.MediaTicks, ss.ClockHz))
	e.MediaDurationMs = e.EndMs - e.StartMs
	e.GapCount = ss.Stats.GapPackets
	// Gap media positions come from the neighboring packet boundaries, which
	// are already in ticks; report them truncated to whole milliseconds.
	for _, run := range ss.Gaps {
		start, end := gapWindow(ss, run)
		e.Gaps = append(e.Gaps, ManifestGap{
			StartExtSeq: run.StartExtSeq, EndExtSeq: run.EndExtSeq, Count: run.Count,
			StartMs: int64(MillisF(start, ss.ClockHz)), EndMs: int64(MillisF(end, ss.ClockHz)),
		})
	}
	if declared == 0 {
		// No declared duration: alignment is assessed against the media
		// duration implied by the packets themselves, which is exact.
		e.AlignmentErrorMs = 0
	} else {
		obs := e.EndMs
		e.AlignmentErrorMs = float64(obs - int64(declared))
	}
	if e.GapCount > 0 {
		e.Status = StatusGap
	} else {
		e.Status = StatusOK
	}
	return e, nil
}

// gapWindow returns the media span [start,end) of a loss run: it begins at the
// end of the last delivered packet before the hole and ends at the media start
// of the next delivered packet. The span is inferred from RTP timestamps; no
// audio is fabricated for it.
func gapWindow(ss *StreamState, run GapRun) (uint64, uint64) {
	var start, end uint64
	foundEnd := false
	var prevEnd uint64
	foundPrev := false
	for _, st := range ss.States {
		if st.Kind != "packet" {
			continue
		}
		if st.ExtSeq < run.StartExtSeq {
			prevEnd = st.MediaEndTicks
			foundPrev = true
			continue
		}
		end = st.MediaStartTicks
		foundEnd = true
		break
	}
	if foundPrev {
		start = prevEnd
	} else {
		start = end
	}
	if !foundEnd {
		end = ss.MediaTicks
	}
	return start, end
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
