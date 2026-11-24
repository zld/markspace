package verify

import (
	"math"
	"sort"

	"mediarc/internal/model"
)

type TrackSummary struct {
	ID         uint32              `json:"id"`
	Type       model.TrackType     `json:"type"`
	Codec      model.Codec         `json:"codec"`
	Samples    int                 `json:"samples"`
	TimeBase   string              `json:"time_base"`
	SampleRate int                 `json:"sample_rate,omitempty"`
	Channels   int                 `json:"channels,omitempty"`
	Layout     model.ChannelLayout `json:"layout,omitempty"`
	Width      int                 `json:"width,omitempty"`
	Height     int                 `json:"height,omitempty"`
	Color      *model.ColorMeta    `json:"color,omitempty"`
	SampleHash string              `json:"sample_hash,omitempty"`
}

type Report struct {
	AVSyncMS             int64          `json:"av_sync_ms"`
	PacketRecoveryRate   float64        `json:"packet_recovery_rate"`
	P95LatencyMS         int64          `json:"p95_latency_ms"`
	InputSampleCount     int            `json:"input_sample_count"`
	OutputSampleCount    int            `json:"output_sample_count"`
	InputTracks          []TrackSummary `json:"input_tracks"`
	OutputTracks         []TrackSummary `json:"output_tracks"`
	SampleHashesMatch    bool           `json:"sample_hashes_match"`
	PropertiesMatch      bool           `json:"properties_match"`
	DeterministicHash    string         `json:"deterministic_hash,omitempty"`
	RewindCount          int            `json:"rewind_count,omitempty"`
	DuplicatePackets     int            `json:"duplicate_packets,omitempty"`
	ReorderedPackets     int            `json:"reordered_packets,omitempty"`
	RecoveredPackets     int            `json:"recovered_packets,omitempty"`
	InjectedLossPackets  int            `json:"injected_loss_packets,omitempty"`
	UnrecoverablePackets int            `json:"unrecoverable_packets,omitempty"`
	ConflictFields       []string       `json:"conflict_fields,omitempty"`
}

func SummarizeTracks(a *model.Archive) []TrackSummary {
	hashes := a.SampleHashes()
	out := make([]TrackSummary, 0, len(a.Tracks))
	for _, tr := range a.Tracks {
		count := 0
		for _, s := range a.Samples {
			if s.TrackID == tr.ID {
				count++
			}
		}
		out = append(out, TrackSummary{
			ID: tr.ID, Type: tr.Type, Codec: tr.Codec, Samples: count,
			TimeBase: timeBaseString(tr.TimeBase), SampleRate: tr.SampleRate,
			Channels: tr.Channels, Layout: tr.Layout, Width: tr.Width, Height: tr.Height,
			Color: tr.Color, SampleHash: hashes[tr.ID],
		})
	}
	return out
}

func timeBaseString(tb model.TimeBase) string {
	return fmtInt64(tb.Num) + "/" + fmtInt64(tb.Den)
}

func fmtInt64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [24]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// Percentile95NearestRank computes ceil(0.95*N)-th smallest latency.
func Percentile95NearestRank(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(math.Ceil(0.95 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// AVSyncDriftMS returns the maximum distance from each video frame to the
// nearest audio sample boundary after exact time-base conversion.
func AVSyncDriftMS(video []model.Sample, videoBase model.TimeBase, audio []model.Sample, audioBase model.TimeBase) int64 {
	if len(video) == 0 || len(audio) == 0 {
		return 0
	}
	audioMS := make([]int64, len(audio))
	for i, s := range audio {
		audioMS[i] = model.NewTimestamp(s.PTS, audioBase).Millis()
	}
	var maxDrift int64
	for _, v := range video {
		pts := model.NewTimestamp(v.PTS, videoBase).Millis()
		best := int64(-1)
		for _, a := range audioMS {
			d := abs64(pts - a)
			if best < 0 || d < best {
				best = d
			}
		}
		if best > maxDrift {
			maxDrift = best
		}
	}
	return maxDrift
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// ColorConflictFields reports all pairwise conflicts and de-duplicates fields.
func ColorConflictFields(t model.Track) []string {
	values := make([]model.ColorMeta, 0, 1+len(t.ColorAlternatives))
	if t.Color != nil {
		values = append(values, *t.Color)
	}
	values = append(values, t.ColorAlternatives...)
	seen := map[string]bool{}
	var fields []string
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			for _, f := range values[i].ConflictFields(values[j]) {
				if !seen[f] {
					seen[f] = true
					fields = append(fields, f)
				}
			}
		}
	}
	sort.Strings(fields)
	return fields
}

// AcceptanceOK checks the public demo thresholds.
func AcceptanceOK(r Report) bool {
	return r.AVSyncMS <= 20 && r.PacketRecoveryRate >= 0.75 && r.P95LatencyMS <= 500
}
