package pipeline

import (
	"fmt"
	"os"

	"rtx/internal/cmaf"
	"rtx/internal/model"
	"rtx/internal/rtp"
)

// RTPSummary is the verified outcome of one source's trace.
type RTPSummary struct {
	Source        string  `json:"source"`
	Packets       int     `json:"packets"`
	Restored      int     `json:"restored"`
	Gaps          int     `json:"gaps"`
	SequenceWraps int     `json:"sequence_wraps"`
	OutOfOrder    int     `json:"out_of_order"`
	TimestampWrap bool    `json:"timestamp_wrap"`
	FirstSec      float64 `json:"first_sec"`
	LastSec       float64 `json:"last_sec"`
}

// RTPVerification is the result of checking all traces against their anchors.
type RTPVerification struct {
	Summaries []RTPSummary       `json:"sources"`
	Switches  []rtp.SourceSwitch `json:"switches"`
}

// verifyRTP loads each source's trace, restores order and proves that every
// packet maps monotonically onto the shared media grid through the explicit
// anchors. Unanchored or ambiguous mappings fail here, before any output is
// written.
func verifyRTP(inputDir string, m *cmaf.Manifest, offsets map[string]int64) (*RTPVerification, error) {
	mediaRate, err := model.NewRateRatio(m.MediaRate.Num, m.MediaRate.Den)
	if err != nil {
		return nil, err
	}
	out := &RTPVerification{}
	var allStreams []*rtp.Stream
	for _, src := range m.Sources {
		if src.RTPTrace == "" {
			continue
		}
		if len(src.RTPAnchors) == 0 {
			return nil, model.E(model.CodeAnchorAmbiguous, "verify rtp",
				"source %s has a trace but no explicit anchors", src.ID)
		}
		abs, err := resolveWithin(inputDir, src.RTPTrace)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, model.E(model.CodeInvalidInput, "verify rtp", "%v", err).WithPath(abs)
		}
		tr, err := rtp.LoadTrace(abs)
		if err != nil {
			return nil, err
		}
		anchors := make([]rtp.Anchor, 0, len(src.RTPAnchors))
		for _, a := range src.RTPAnchors {
			anchors = append(anchors, rtp.Anchor{
				Source: a.Source, RTPTimestamp: a.RTPTimestamp, Wrap: a.Wrap,
				MediaTicks: a.MediaTicks, ClockRate: a.ClockRate,
			})
		}
		mapping, err := rtp.NewMapping(mediaRate, anchors)
		if err != nil {
			return nil, err
		}
		streams, err := rtp.Reorder(tr)
		if err != nil {
			return nil, err
		}
		allStreams = append(allStreams, streams...)
		for _, stream := range streams {
			times, err := mapping.MapStream(stream)
			if err != nil {
				return nil, err
			}
			sum := RTPSummary{
				Source:        stream.Source,
				Packets:       len(stream.Packets),
				Restored:      len(stream.Packets),
				Gaps:          len(stream.Gaps),
				SequenceWraps: stream.Wraps,
				OutOfOrder:    stream.OutOfOrder,
			}
			if len(times) > 0 {
				sum.FirstSec = times[0].Seconds()
				sum.LastSec = times[len(times)-1].Seconds()
			}
			for i := 1; i < len(stream.Packets); i++ {
				if stream.Packets[i].TS < stream.Packets[i-1].TS {
					sum.TimestampWrap = true
					break
				}
			}
			out.Summaries = append(out.Summaries, sum)
		}
	}
	// Detect switches across every source's streams, not just within one trace.
	if len(allStreams) > 1 {
		mediaRate, err := model.NewRateRatio(m.MediaRate.Num, m.MediaRate.Den)
		if err != nil {
			return nil, err
		}
		anchors := make([]rtp.Anchor, 0)
		for _, src := range m.Sources {
			for _, a := range src.RTPAnchors {
				anchors = append(anchors, rtp.Anchor{
					Source: a.Source, RTPTimestamp: a.RTPTimestamp, Wrap: a.Wrap,
					MediaTicks: a.MediaTicks, ClockRate: a.ClockRate,
				})
			}
		}
		mapping, err := rtp.NewMapping(mediaRate, anchors)
		if err != nil {
			return nil, err
		}
		switches, err := mapping.DetectSwitches(allStreams)
		if err != nil {
			return nil, err
		}
		out.Switches = switches
	}
	// Cross-check that RTP switches agree with declared manifest switches.
	declared := 0
	for _, e := range m.Events {
		if e.Kind == cmaf.EventSourceSwitch {
			declared++
		}
	}
	if declared != len(out.Switches) && len(m.Sources) > 1 {
		return nil, model.E(model.CodeAnchorMismatch, "verify rtp",
			"manifest declares %d source switches but anchors imply %d", declared, len(out.Switches))
	}
	return out, nil
}

// RTPLine renders one source's RTP facts for terminal output.
func (s RTPSummary) RTPLine() string {
	return fmt.Sprintf("rtp source=%s packets=%d gaps=%d seq_wraps=%d out_of_order=%d ts_wrap=%d span=%.3f-%.3fs",
		s.Source, s.Packets, s.Gaps, s.SequenceWraps, s.OutOfOrder, boolInt(s.TimestampWrap), s.FirstSec, s.LastSec)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
