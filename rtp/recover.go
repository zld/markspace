package rtp

import (
	"fmt"
	"sort"

	"mediarc/model"
	"mediarc/timebase"
)

// SegmentKind distinguishes recovered media from reconstructed silence.
type SegmentKind int

const (
	// KindFrame is real payload that was delivered.
	KindFrame SegmentKind = iota
	// KindSilence is a gap filled with silence.
	KindSilence
)

// String renders the segment kind for reports.
func (k SegmentKind) String() string {
	if k == KindSilence {
		return "silence"
	}
	return "frame"
}

// Segment is one contiguous piece of the recovered audio timeline.
type Segment struct {
	Kind SegmentKind
	// StartNS and EndNS delimit the segment on the shared timeline.
	StartNS int64
	EndNS   int64
	// DurationNS is EndNS-StartNS.
	DurationNS int64
	// FirstSeq and LastSeq are the extended sequence numbers the segment
	// covers. For silence they are the missing packet range.
	FirstSeq uint64
	LastSeq  uint64
	// Payload is the delivered bytes for a frame; nil for silence.
	Payload []byte
	// Source is the gap that produced a silence segment.
	Source Gap
}

// RecoveryConfig controls how gaps are filled.
type RecoveryConfig struct {
	// Silent is true when missing packets are reconstructed as silence.
	// When false a gap is still recorded but no segment is emitted.
	Silent bool
	// SampleRate and Channels describe the silence to synthesise. They only
	// affect the reported shape, never the duration.
	SampleRate int
	Channels   int
}

func (c RecoveryConfig) validate() (RecoveryConfig, error) {
	if c.SampleRate < 0 || c.Channels < 0 {
		return RecoveryConfig{}, model.New(model.CodeInvalidArgument, stream,
			"sample rate and channels must not be negative")
	}
	if c.SampleRate == 0 {
		c.SampleRate = 48000
	}
	if c.Channels == 0 {
		c.Channels = 2
	}
	return c, nil
}

// RecoveryStats summarises gap repair.
type RecoveryStats struct {
	// PacketsLost is the number of missing packets seen across all gaps.
	PacketsLost uint64
	// Gaps is the number of gap runs.
	Gaps int
	// Recovered is the number of silence segments emitted.
	Recovered int
	// RecoveredNS is the total media time reconstructed as silence.
	RecoveredNS int64
}

// Recovery merges delivered frames and emerging gaps into one ordered segment
// list. It is deterministic: the same release sequence always yields the same
// segments and hashes.
type Recovery struct {
	cfg   RecoveryConfig
	stats RecoveryStats
}

// NewRecovery validates cfg.
func NewRecovery(cfg RecoveryConfig) (*Recovery, error) {
	validated, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	return &Recovery{cfg: validated}, nil
}

// Stats returns the current counters.
func (r *Recovery) Stats() RecoveryStats { return r.stats }

// Apply converts one window release into ordered segments. Frames and silence
// never overlap: a silence segment covers exactly the missing packet duration.
func (r *Recovery) Apply(rel Release) []Segment {
	// Merge by sequence position so the output order matches the source.
	type item struct {
		seq   uint64
		frame *Frame
		gap   *Gap
	}
	items := make([]item, 0, len(rel.Frames)+len(rel.Gaps))
	for i := range rel.Frames {
		items = append(items, item{seq: rel.Frames[i].ExtendedSeq, frame: &rel.Frames[i]})
	}
	for i := range rel.Gaps {
		items = append(items, item{seq: rel.Gaps[i].StartSeq, gap: &rel.Gaps[i]})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].seq < items[j].seq })

	segments := make([]Segment, 0, len(items))
	for _, it := range items {
		if it.frame != nil {
			f := it.frame
			segments = append(segments, Segment{
				Kind:       KindFrame,
				StartNS:    f.StartNS,
				EndNS:      f.EndNS,
				DurationNS: f.DurationNS,
				FirstSeq:   f.ExtendedSeq,
				LastSeq:    f.ExtendedSeq,
				Payload:    f.Payload,
			})
			continue
		}
		g := it.gap
		r.stats.Gaps++
		r.stats.PacketsLost += g.Count
		if !r.cfg.Silent {
			continue
		}
		start := int64(0)
		if len(segments) > 0 {
			start = segments[len(segments)-1].EndNS
		}
		end := start + g.DurationNS
		r.stats.Recovered++
		r.stats.RecoveredNS += g.DurationNS
		segments = append(segments, Segment{
			Kind:       KindSilence,
			StartNS:    start,
			EndNS:      end,
			DurationNS: g.DurationNS,
			FirstSeq:   g.StartSeq,
			LastSeq:    g.StartSeq + g.Count - 1,
			Source:     *g,
		})
	}
	return segments
}

// SilenceSample builds the deterministic silence samples a segment stands for.
// It is exported so the renderer can hash it without duplicating the policy.
func (r *Recovery) SilenceSample(seg Segment) []byte {
	if seg.Kind != KindSilence {
		return nil
	}
	frames := seg.DurationNS * int64(r.cfg.SampleRate) / 1_000_000_000
	samples := frames * int64(r.cfg.Channels)
	if samples <= 0 {
		return nil
	}
	return make([]byte, samples*2) // 16-bit little-endian zero samples
}

// Describe renders a segment for the acceptance report.
func (s Segment) Describe() string {
	return fmt.Sprintf("%s seq=%d..%d %s+%d", s.Kind,
		s.FirstSeq, s.LastSeq, timebase.FormatNanos(s.StartNS), s.DurationNS)
}
