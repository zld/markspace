package sync

import (
	"fmt"
	"sort"

	"mediapipe/internal/mp4"
)

// LayoutIssue codes. Each issue is locatable: it names the fragment sequence,
// the track and the exact field, and it never rewrites the source value.
const (
	IssueChannelCount  = "channel_count_mismatch"
	IssueChannelLayout = "channel_layout_mismatch"
	IssueSampleRate    = "sample_rate_mismatch"
	IssueSampleSize    = "sample_size_mismatch"
	IssueColorChange   = "color_metadata_changed"
	IssueMissingDecl   = "layout_declaration_missing"
	IssueTrackKind     = "track_kind_mismatch"
	IssueMissingGap    = "transport_gap_in_track"
	IssueRateTimebase  = "sample_rate_timescale"
)

// TrackDecl is the audio/video declaration of one fragment.
type TrackDecl struct {
	Segment      uint32
	TrackID      uint32
	Handler      string
	Channels     uint16
	Timescale    uint32
	SampleRate   uint32
	SampleSize   uint16
	Width        uint16
	Height       uint16
	Color        *mp4.Colr
	SampleCount  int
	PayloadRates []uint32 // observed sample rates, if samples declare one
}

// AudioExpectation is the reference audio layout for a track.
type AudioExpectation struct {
	Channels   uint16
	SampleRate uint32
	SampleSize uint16
	// ChannelLayout is the canonical order, e.g. "FL,FR" or "FL,FR,FC,LFE".
	ChannelLayout string
}

// CheckResult collects layout diagnostics.
type CheckResult struct {
	Issues   []Diagnostic
	Segments int
	Tracks   int
}

// CheckLayout compares every fragment's declarations against the reference and
// against each other. Differences are reported, never remapped.
func CheckLayout(decls []TrackDecl, audio map[uint32]AudioExpectation, video map[uint32]bool) CheckResult {
	var res CheckResult
	seenTracks := map[uint32]bool{}
	lastColor := map[uint32]*mp4.Colr{}
	for _, d := range decls {
		seenTracks[d.TrackID] = true
		isAudio := d.Handler == "soun"
		if isAudio != (d.Channels != 0) {
			res.Issues = append(res.Issues, Diagnostic{
				Code: IssueTrackKind, TrackID: d.TrackID, Segment: d.Segment, Field: "handler", Severity: "error",
				Message: fmt.Sprintf("handler %q inconsistent with channel declaration %d", d.Handler, d.Channels),
			})
		}
		if isAudio {
			exp, ok := audio[d.TrackID]
			if !ok {
				res.Issues = append(res.Issues, Diagnostic{
					Code: IssueMissingDecl, TrackID: d.TrackID, Segment: d.Segment, Field: "audio", Severity: "error",
					Message: "no reference audio layout for track",
				})
				continue
			}
			if d.Channels != exp.Channels {
				res.Issues = append(res.Issues, Diagnostic{
					Code: IssueChannelCount, TrackID: d.TrackID, Segment: d.Segment, Field: "channel_count", Severity: "error",
					Message: fmt.Sprintf("declared %d channels, reference %d", d.Channels, exp.Channels),
				})
			}
			if exp.ChannelLayout != "" && int(exp.Channels) != len(splitLayout(exp.ChannelLayout)) {
				res.Issues = append(res.Issues, Diagnostic{
					Code: IssueChannelLayout, TrackID: d.TrackID, Segment: d.Segment, Field: "channel_layout", Severity: "error",
					Message: fmt.Sprintf("reference layout %q has %d positions for %d channels",
						exp.ChannelLayout, len(splitLayout(exp.ChannelLayout)), exp.Channels),
				})
			}
			if d.SampleRate != exp.SampleRate {
				res.Issues = append(res.Issues, Diagnostic{
					Code: IssueSampleRate, TrackID: d.TrackID, Segment: d.Segment, Field: "sample_rate", Severity: "error",
					Message: fmt.Sprintf("declared %d Hz, reference %d Hz", d.SampleRate, exp.SampleRate),
				})
			}
			if exp.SampleSize != 0 && d.SampleSize != exp.SampleSize {
				res.Issues = append(res.Issues, Diagnostic{
					Code: IssueSampleSize, TrackID: d.TrackID, Segment: d.Segment, Field: "sample_size", Severity: "error",
					Message: fmt.Sprintf("declared %d bits, reference %d bits", d.SampleSize, exp.SampleSize),
				})
			}
			// An audio track whose mdhd timescale disagrees with its sample
			// entry rate has an inconsistent sample clock: every duration
			// derived from one of them is wrong. The mismatch is reported, not
			// repaired by choosing a winner.
			if d.Timescale != 0 && d.Timescale != exp.SampleRate {
				res.Issues = append(res.Issues, Diagnostic{
					Code: IssueRateTimebase, TrackID: d.TrackID, Segment: d.Segment, Field: "timescale", Severity: "error",
					Message: fmt.Sprintf("mdhd timescale %d Hz != sample entry rate %d Hz", d.Timescale, exp.SampleRate),
				})
			}
			for _, rate := range d.PayloadRates {
				if rate != exp.SampleRate {
					res.Issues = append(res.Issues, Diagnostic{
						Code: IssueSampleRate, TrackID: d.TrackID, Segment: d.Segment, Field: "sample_rate_payload", Severity: "error",
						Message: fmt.Sprintf("track samples imply %d Hz, declaration %d Hz", rate, exp.SampleRate),
					})
				}
			}
		} else if video[d.TrackID] || d.Width != 0 {
			if d.Width == 0 || d.Height == 0 {
				res.Issues = append(res.Issues, Diagnostic{
					Code: IssueMissingDecl, TrackID: d.TrackID, Segment: d.Segment, Field: "width/height", Severity: "error",
					Message: "visual fragment has no size declaration",
				})
			}
			prev, ok := lastColor[d.TrackID]
			if ok && d.Color != nil && !d.Color.Equal(*prev) {
				res.Issues = append(res.Issues, Diagnostic{
					Code: IssueColorChange, TrackID: d.TrackID, Segment: d.Segment, Field: "colr", Severity: "error",
					Message: fmt.Sprintf("color changed from %+v to %+v", *prev, *d.Color),
				})
			}
			if d.Color != nil {
				cp := *d.Color
				lastColor[d.TrackID] = &cp
			}
		}
	}
	res.Segments = len(decls)
	res.Tracks = len(seenTracks)
	sort.SliceStable(res.Issues, func(i, j int) bool {
		if res.Issues[i].TrackID != res.Issues[j].TrackID {
			return res.Issues[i].TrackID < res.Issues[j].TrackID
		}
		return res.Issues[i].Segment < res.Issues[j].Segment
	})
	return res
}

func splitLayout(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// DeclsFromFile extracts per-fragment layout declarations from a parsed file.
func DeclsFromFile(f *mp4.File) []TrackDecl {
	var out []TrackDecl
	for _, seg := range f.Segments {
		for _, st := range seg.Tracks {
			t := f.TrackByID(st.TrackID)
			if t == nil {
				continue
			}
			d := TrackDecl{Segment: seg.Sequence, TrackID: st.TrackID, Handler: t.Handler,
				Timescale: t.Timescale, SampleCount: len(st.Samples)}
			if t.Audio != nil {
				d.Channels = t.Audio.ChannelCount
				d.SampleRate = t.Audio.SampleRate
				d.SampleSize = t.Audio.SampleSize
			}
			if t.Video != nil {
				d.Width = t.Video.Width
				d.Height = t.Video.Height
				d.Color = t.Video.Color
			}
			out = append(out, d)
		}
	}
	return out
}

// GapDiagnostics turns transport missing ranges into locatable diagnostics.
func GapDiagnostics(trackID uint32, missing []MissingRange) []Diagnostic {
	var out []Diagnostic
	for _, m := range missing {
		out = append(out, Diagnostic{
			Code: IssueMissingGap, TrackID: trackID, Field: "transport", Severity: "error",
			Message: fmt.Sprintf("missing media [%d..%d] unified ticks, %d packets, source %s",
				m.StartTicks, m.EndTicks, m.Packets, m.Source),
		})
	}
	return out
}
