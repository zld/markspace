package pipeline

import (
	"encoding/json"

	"mediadist/internal/merr"
	"mediadist/internal/track"
)

// reportDoc is the on-disk evidence format. Fields are ordered for readable
// diffs and every hash is hex so two runs can be compared byte for byte.
type reportDoc struct {
	Options  reportOptions  `json:"options"`
	Metrics  reportMetrics  `json:"metrics"`
	Index    IndexReport    `json:"index"`
	Mapping  string         `json:"mapping"`
	Tracks   []TrackReport  `json:"tracks"`
	Streams  []StreamReport `json:"streams"`
	Segments []SegmentReport `json:"segments"`
	Remux    reportRemux    `json:"remux"`
	Errors   []reportError  `json:"errors"`
}

type reportOptions struct {
	Seed          int64  `json:"seed"`
	LossBurst     int    `json:"loss_burst"`
	BandwidthKbps int    `json:"bandwidth_kbps"`
	SegmentMS     int    `json:"segment_ms"`
	UnknownTracks string `json:"unknown_tracks"`
	RepeatRuns    int    `json:"repeat_runs"`
}

type reportMetrics struct {
	RoundtripMatch   int     `json:"roundtrip_match"`
	RoundtripTotal   int     `json:"roundtrip_total"`
	RoundtripRate    float64 `json:"roundtrip_rate"`
	KeyframeHit      int     `json:"keyframe_hit"`
	KeyframeTotal    int     `json:"keyframe_total"`
	KeyframeRate     float64 `json:"keyframe_rate"`
	DuplicatePackets int     `json:"duplicate_packets"`
	ReorderedPackets int     `json:"reordered_packets"`
	RecoveredGaps    int     `json:"recovered_gaps"`
	DamagedSpans     int     `json:"damaged_spans"`
	RepeatRunMatch   bool    `json:"repeat_run_match"`
	SegmentSHA256    string  `json:"segment_sha256"`
	ReadBackMatch    int     `json:"readback_match"`
	ReadBackTotal    int     `json:"readback_total"`
}

type reportRemux struct {
	Path           string `json:"path"`
	Size           int64  `json:"size"`
	SampleCount    int    `json:"sample_count"`
	MovieTimescale uint32 `json:"movie_timescale"`
	SHA256         string `json:"sha256"`
}

type reportError struct {
	Stage     string `json:"stage"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Stream    int    `json:"stream"`
	Seq       int64  `json:"seq"`
	Offset    int64  `json:"offset"`
	Length    int64  `json:"length"`
	TrackID   uint32 `json:"track_id,omitempty"`
	Recovered bool   `json:"recovered"`
}

// MarshalReport renders the evidence as canonical JSON.
func MarshalReport(res *RunResult) ([]byte, error) {
	doc := reportDoc{
		Options: reportOptions{
			Seed: res.Options.Seed, LossBurst: res.Options.LossBurst,
			BandwidthKbps: res.Options.BandwidthKbps, SegmentMS: res.Options.SegmentMS,
			UnknownTracks: unknownPolicyName(res.Options.UnknownTracks),
			RepeatRuns:    res.Options.RepeatRuns,
		},
		Metrics: reportMetrics{
			RoundtripMatch: res.Metrics.RoundtripMatch, RoundtripTotal: res.Metrics.RoundtripTotal,
			RoundtripRate: res.Metrics.RoundtripRate(),
			KeyframeHit:   res.Metrics.KeyframeHit, KeyframeTotal: res.Metrics.KeyframeTotal,
			KeyframeRate:     res.Metrics.KeyframeRate(),
			DuplicatePackets: res.Metrics.DuplicatePackets, ReorderedPackets: res.Metrics.ReorderedPackets,
			RecoveredGaps: res.Metrics.RecoveredGaps, DamagedSpans: res.Metrics.DamagedSpans,
			RepeatRunMatch: res.Metrics.RepeatRunMatch, SegmentSHA256: res.Metrics.SegmentSHA256,
			ReadBackMatch: res.ReadBackMatch, ReadBackTotal: res.ReadBackTotal,
		},
		Index:    res.Index,
		Mapping:  res.Mapping,
		Tracks:   res.Tracks,
		Streams:  res.Streams,
		Segments: res.Segments,
	}
	if res.Remux != nil {
		doc.Remux = reportRemux{
			Path: res.Remux.Path, Size: res.Remux.Size, SampleCount: res.Remux.SampleCount,
			MovieTimescale: res.Remux.MovieTimescale, SHA256: res.Remux.SHA256,
		}
	}
	doc.Errors = make([]reportError, 0, len(res.Errors))
	for _, e := range res.Errors {
		doc.Errors = append(doc.Errors, reportError{
			Stage: string(e.Stage), Code: e.Code, Message: e.Message,
			Stream: e.Span.Stream, Seq: e.Span.Seq, Offset: e.Span.Offset,
			Length: e.Span.Length, TrackID: e.Span.TrackID, Recovered: e.Recovered,
		})
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, merr.Wrap(merr.StagePipeline, "report_encode", merr.NoSpan(0), false, err,
			"encoding report")
	}
	return append(body, '\n'), nil
}

func unknownPolicyName(p track.UnknownPolicy) string {
	if p == track.UnknownDrop {
		return "drop"
	}
	return "error"
}
