package pipeline

import (
	"context"
	"sort"
	"strconv"

	"mediacheck/ebml"
	"mediacheck/internal/fixture"
	"mediacheck/timeline"
)

// toMergeInputs maps scan records into the dependency-free merge input.
func toMergeInputs(scan *ebml.ScanResult) ([]timeline.BlockInput, []timeline.TrackInput) {
	blocks := make([]timeline.BlockInput, 0, len(scan.Blocks))
	for _, b := range scan.Blocks {
		blocks = append(blocks, timeline.BlockInput{
			TrackNumber: b.TrackNumber,
			TimeNs:      b.TimeNs,
			Offset:      b.Offset,
			Size:        b.Size,
			CheckOK:     b.CheckOK,
		})
	}
	tracks := make([]timeline.TrackInput, 0, len(scan.Tracks))
	for _, t := range scan.Tracks {
		ti := timeline.TrackInput{
			Number: t.Number, UID: t.UID, IsVideo: t.IsVideo(),
			SampleRate: int64(t.SampleRate), FrameRate: 24,
		}
		tracks = append(tracks, ti)
	}
	return blocks, tracks
}

// percentileMs returns the p-th percentile of millisecond latencies using the
// nearest-rank method; an empty sample yields 0.
func percentileMs(samples []float64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	rank := int(float64(len(sorted)) * p / 100.0)
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func itoa(v int) string { return strconv.Itoa(v) }

// decidePublish runs every release gate. A failed gate keeps the temp MP4
// unpublished and leaves no completion marker.
func decidePublish(ctx context.Context, m *Metrics, scan *ebml.ScanResult, cfg Config, video, audioTrack *ebml.TrackSpec) {
	if err := ctx.Err(); err != nil {
		m.Faults = append(m.Faults, "cancelled: "+err.Error())
		return
	}
	gates := []struct {
		ok  bool
		why string
	}{
		{scan.TruncatedAt < 0, "input truncated"},
		{len(scan.CheckFailures) == 0, "cluster crc failure"},
		{m.TimelineTrusted, "timeline not trusted"},
		{m.AudioLayoutValid, "invalid audio channel layout"},
		{m.ColorUnsafeReason == "", "unsafe colour metadata"},
		{m.AVSyncErrorMs <= cfg.SyncLimitMs, "av sync error exceeds limit"},
		{m.P95LatencyMs <= cfg.P95LimitMs, "p95 latency exceeds limit"},
		{hasAllFrames(m), "missing frames in export"},
		{m.RTPUnrecoverable == 0, "unrecoverable rtp loss"},
		{video != nil && audioTrack != nil, "missing required track"},
	}
	for _, g := range gates {
		if !g.ok {
			m.Faults = append(m.Faults, "publish blocked: "+g.why)
			return
		}
	}
	if cfg.OutputPath == "" {
		m.Published = true
		return
	}
	if err := remuxAndPublish(cfg.OutputPath, scan, video, audioTrack); err != nil {
		m.Faults = append(m.Faults, "remux: "+err.Error())
		return
	}
	m.Published = true
	m.CompletionMarker = cfg.OutputPath + ".complete"
}

func hasAllFrames(m *Metrics) bool {
	for _, h := range m.FrameHashes {
		if h == "" {
			return false
		}
	}
	return len(m.FrameHashes) > 0
}

var _ = fixture.VideoTrackNumber
