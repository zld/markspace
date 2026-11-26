package timeline

import (
	"fmt"
	"sort"

	"mediasync/internal/ingest"
	"mediasync/internal/media"
)

const (
	MaxLossFraction = 0.10
	MaxSubtitleMS   = int64(1)
	OpusFrameTicks  = 960 * 147 // 20 ms at 48 kHz
)

type EventKind string

const (
	KindOpus     EventKind = "opus"
	KindAAC      EventKind = "aac"
	KindGap      EventKind = "gap"
	KindSubtitle EventKind = "subtitle"
)

type Event struct {
	Index       int       `json:"index"`
	TrackOrder  int       `json:"track_order"`
	TrackID     string    `json:"track_id"`
	Kind        EventKind `json:"kind"`
	RecordIndex int       `json:"record_index"`
	Sequence    int64     `json:"sequence,omitempty"`
	StartTicks  int64     `json:"start_ticks"`
	EndTicks    int64     `json:"end_ticks"`
	Text        string    `json:"text,omitempty"`
}

type Stats struct {
	Tracks                 int     `json:"tracks"`
	Unique                 int     `json:"unique"`
	Duplicates             int     `json:"duplicates"`
	Gaps                   int     `json:"gaps"`
	LossFraction           float64 `json:"loss_fraction"`
	LossPercent            string  `json:"loss_percent"`
	AACSamples             int     `json:"aac_samples"`
	Subtitles              int     `json:"subtitles"`
	MaxSubtitleDeviationMS int64   `json:"max_subtitle_deviation_ms"`
}

type QualityReport struct {
	LossFraction           float64  `json:"loss_fraction"`
	LossLimit              float64  `json:"loss_limit"`
	MaxSubtitleDeviationMS int64    `json:"max_subtitle_deviation_ms"`
	SubtitleLimitMS        int64    `json:"subtitle_limit_ms"`
	OK                     bool     `json:"ok"`
	Reasons                []string `json:"reasons,omitempty"`
}

type Timeline struct {
	TicksPerSecond int64         `json:"ticks_per_second"`
	Events         []Event       `json:"events"`
	Stats          Stats         `json:"stats"`
	Quality        QualityReport `json:"quality"`
}

type Input struct {
	Spec      ingest.TrackSpec
	Opus      *ingest.OpusResult
	AAC       *ingest.AACResult
	Subtitles *ingest.SubtitleFile
}

// Merge converts every track to the common tick scale and performs one
// deterministic sort by time, manifest track order/id and record sequence.
func Merge(inputs []Input) (*Timeline, error) {
	tl := &Timeline{TicksPerSecond: media.TicksPerSecond}
	stats := Stats{Tracks: len(inputs)}
	events := make([]Event, 0)

	for order, in := range inputs {
		switch in.Spec.Kind {
		case ingest.KindOpus:
			if in.Opus == nil {
				return nil, media.InputError("merge", in.Spec.ID, fmt.Errorf("missing Opus result"))
			}
			stats.Unique += in.Opus.Stats.Unique
			stats.Duplicates += in.Opus.Stats.Duplicates
			stats.Gaps += in.Opus.Stats.Gaps
			baseExt := int64(0)
			baseStart := int64(0)
			if len(in.Opus.Packets) > 0 {
				baseExt = in.Opus.Packets[0].ExtSequence
				baseStart = in.Opus.Packets[0].StartTicks
			}
			endByExt := make(map[int64]int64, len(in.Opus.Packets))
			for i, p := range in.Opus.Packets {
				end, err := media.CheckedAdd(p.StartTicks, OpusFrameTicks)
				if err != nil {
					return nil, media.OverflowError("merge", in.Spec.ID, err).WithPacket(int(p.Ordinal))
				}
				if i+1 < len(in.Opus.Packets) {
					next := in.Opus.Packets[i+1]
					if next.ExtSequence == p.ExtSequence+1 && next.StartTicks > p.StartTicks {
						end = next.StartTicks
					}
				}
				endByExt[p.ExtSequence] = end
				events = append(events, Event{
					TrackOrder: order, TrackID: in.Spec.ID, Kind: KindOpus,
					RecordIndex: p.Ordinal, Sequence: p.ExtSequence,
					StartTicks: p.StartTicks, EndTicks: end,
				})
			}
			for _, gapSeq := range in.Opus.Stats.GapSequences {
				delta := gapSeq - baseExt
				start, err := media.CheckedMul(delta, OpusFrameTicks)
				if err == nil {
					start, err = media.CheckedAdd(baseStart, start)
				}
				if err != nil {
					return nil, media.OverflowError("merge", in.Spec.ID, err).WithPacket(int(gapSeq))
				}
				if measuredEnd, ok := endByExt[gapSeq-1]; ok {
					start = measuredEnd
				}
				end, err := media.CheckedAdd(start, OpusFrameTicks)
				if err != nil {
					return nil, media.OverflowError("merge", in.Spec.ID, err).WithPacket(int(gapSeq))
				}
				events = append(events, Event{
					TrackOrder: order, TrackID: in.Spec.ID, Kind: KindGap,
					Sequence: gapSeq, StartTicks: start, EndTicks: end,
				})
			}

		case ingest.KindAAC:
			if in.AAC == nil {
				return nil, media.InputError("merge", in.Spec.ID, fmt.Errorf("missing AAC result"))
			}
			stats.AACSamples += len(in.AAC.Samples)
			for _, s := range in.AAC.Samples {
				end, err := media.CheckedAdd(s.StartTicks, s.DurationTicks)
				if err != nil {
					return nil, media.OverflowError("merge", in.Spec.ID, err).WithRecord(s.Index).WithOffset(s.Offset)
				}
				events = append(events, Event{
					TrackOrder: order, TrackID: in.Spec.ID, Kind: KindAAC,
					RecordIndex: s.Index, StartTicks: s.StartTicks, EndTicks: end,
				})
			}

		case ingest.KindSubtitle:
			if in.Subtitles == nil {
				return nil, media.InputError("merge", in.Spec.ID, fmt.Errorf("missing subtitle result"))
			}
			stats.Subtitles += len(in.Subtitles.Cues)
			for _, c := range in.Subtitles.Cues {
				start, err := media.FromMilliseconds(c.StartMS)
				if err != nil {
					return nil, media.OverflowError("merge", in.Spec.ID, err).WithRecord(c.Index)
				}
				end, err := media.FromMilliseconds(c.EndMS)
				if err != nil {
					return nil, media.OverflowError("merge", in.Spec.ID, err).WithRecord(c.Index)
				}
				if c.ReferenceStartMS != nil {
					dev := c.StartMS - *c.ReferenceStartMS
					if dev < 0 {
						dev = -dev
					}
					if dev > stats.MaxSubtitleDeviationMS {
						stats.MaxSubtitleDeviationMS = dev
					}
				}
				events = append(events, Event{
					TrackOrder: order, TrackID: in.Spec.ID, Kind: KindSubtitle,
					RecordIndex: c.Index, StartTicks: start, EndTicks: end, Text: c.Text,
				})
			}
		}
	}

	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.StartTicks != b.StartTicks {
			return a.StartTicks < b.StartTicks
		}
		if a.TrackOrder != b.TrackOrder {
			return a.TrackOrder < b.TrackOrder
		}
		if a.TrackID != b.TrackID {
			return a.TrackID < b.TrackID
		}
		if a.Sequence != b.Sequence {
			return a.Sequence < b.Sequence
		}
		return a.RecordIndex < b.RecordIndex
	})
	for i := range events {
		events[i].Index = i
	}

	denom := stats.Unique + stats.Gaps
	if denom > 0 {
		stats.LossFraction = float64(stats.Gaps) / float64(denom)
	}
	stats.LossPercent = fmt.Sprintf("%.2f%%", stats.LossFraction*100)
	tl.Events = events
	tl.Stats = stats
	tl.Quality = evaluateQuality(stats, 0, 1)
	return tl, nil
}

func evaluateQuality(stats Stats, lossLimit float64, subtitleLimitMS int64) QualityReport {
	if lossLimit == 0 {
		lossLimit = MaxLossFraction
	}
	if subtitleLimitMS == 0 {
		subtitleLimitMS = MaxSubtitleMS
	}
	q := QualityReport{
		LossFraction: stats.LossFraction, LossLimit: lossLimit,
		MaxSubtitleDeviationMS: stats.MaxSubtitleDeviationMS,
		SubtitleLimitMS:        subtitleLimitMS, OK: true,
	}
	// Integer comparison keeps the exact 1/10 acceptance case on the passing
	// side of the <= 10% threshold.
	denom := stats.Unique + stats.Gaps
	if denom > 0 && stats.Gaps*10 > denom {
		q.OK = false
		q.Reasons = append(q.Reasons, fmt.Sprintf("loss %.2f%% exceeds %.2f%%", stats.LossFraction*100, lossLimit*100))
	}
	if stats.MaxSubtitleDeviationMS > subtitleLimitMS {
		q.OK = false
		q.Reasons = append(q.Reasons, fmt.Sprintf("subtitle deviation %dms exceeds %dms", stats.MaxSubtitleDeviationMS, subtitleLimitMS))
	}
	return q
}

// CheckQuality converts a failed report into the required structured exit 3.
func (t *Timeline) CheckQuality() error {
	if t.Quality.OK {
		return nil
	}
	return media.QualityError("quality_gate", "", fmt.Errorf("%s", joinReasons(t.Quality.Reasons)))
}

func joinReasons(reasons []string) string {
	out := ""
	for i, r := range reasons {
		if i > 0 {
			out += "; "
		}
		out += r
	}
	if out == "" {
		out = "quality threshold failed"
	}
	return out
}
