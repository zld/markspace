package demo

import (
	"context"
	"fmt"
	"os"

	"mediarc/model"
	rtpkernel "mediarc/rtp"
	"mediarc/sample"
	"mediarc/subtitle"
	synckernel "mediarc/sync"
)

// alignmentSRT holds two caption cues on the millisecond grid the subtitle
// formats allow, deliberately a few hundred microseconds off the 29.97 fps
// frame grid the RTP anchors sit on.
const alignmentSRT = "1\n00:00:01,034 --> 00:00:01,900\nframe-near cue\n\n" +
	"2\n00:00:02,002 --> 00:00:02,800\nsecond cue\n\n"

// anchorFrameTicks is one 29.97 fps frame in 90 kHz RTP ticks (3003).
const anchorFrameTicks = 3003

func (r *runner) runAlignment(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return model.New(model.CodeCanceled, "demo", "canceled before alignment").WithCause(err)
	}
	if err := os.WriteFile(r.path("alignment.srt"), []byte(alignmentSRT), 0o644); err != nil {
		return model.Fatal(model.CodeInvalidArgument, "demo", "cannot write alignment fixture").WithCause(err)
	}
	r.addFixture("alignment.srt")
	parsed := subtitle.ParseSRT([]byte(alignmentSRT))
	if len(parsed.Cues) != 2 {
		return model.New(model.CodeInvalidCue, "demo", "alignment fixture failed to parse")
	}
	cueRefs := make([]synckernel.CueRef, 0, len(parsed.Cues))
	for i, cue := range parsed.Cues {
		cueRefs = append(cueRefs, synckernel.CueRef{Index: i + 1, StartNS: cue.Start, EndNS: cue.End})
	}

	// Anchors are the delivered starts of an RTP stream packetised at the
	// 29.97 fps frame grid, i.e. the coarsest grid the archive's audio clock
	// really provides.
	anchorFixture, err := sample.BuildRTP(r.fixtureDir, "anchors.rtp", sample.RTPOptions{
		Packets: 100, StartSeq: 0, StartTimestamp: 0,
		PacketDurationNS: 33_366_667, ClockRate: 90000, TimestampJumpAt: -1,
	})
	if err != nil {
		return model.Fatal(model.CodeInvalidArgument, "demo", "cannot build anchor stream").WithCause(err)
	}
	r.addFixture("anchors.rtp")
	packets, err := readPackets(anchorFixture.Path)
	if err != nil {
		return err
	}
	window, err := rtpkernel.NewWindow(rtpkernel.Config{
		Depth: r.opts.Window, PacketDurationNS: 33_366_667, ClockRate: 90000,
	})
	if err != nil {
		return err
	}
	var anchors []synckernel.Anchor
	for index, packet := range packets {
		release, err := window.Push(rtpkernel.Arrival{Packet: packet, ReceivedNS: int64(index) * 1_000_000})
		if err != nil && !expectedPushError(err) {
			return err
		}
		for _, frame := range release.Frames {
			anchors = append(anchors, synckernel.Anchor{TimeNS: frame.StartNS, Label: "rtp"})
		}
	}
	for _, frame := range window.Flush().Frames {
		anchors = append(anchors, synckernel.Anchor{TimeNS: frame.StartNS, Label: "rtp"})
	}
	if len(anchors) == 0 {
		return model.New(model.CodeAlignmentDrift, "demo", "anchor stream produced no anchors")
	}

	aligner, err := synckernel.New(synckernel.Config{ToleranceNS: synckernel.DefaultTolerance, RequireAnchor: true})
	if err != nil {
		return err
	}
	result := aligner.Align(cueRefs, anchors)

	// Independently compute the nearest-anchor distance by linear scan, so the
	// aligner's answer is cross-checked rather than compared to a constant.
	var expectedMax int64
	for _, ref := range cueRefs {
		best := int64(-1)
		for _, anchor := range anchors {
			diff := ref.StartNS - anchor.TimeNS
			if diff < 0 {
				diff = -diff
			}
			if best < 0 || diff < best {
				best = diff
			}
		}
		if best > expectedMax {
			expectedMax = best
		}
	}
	checks := []Assertion{
		assert("every caption cue finds an anchor", fmt.Sprint(result.Unanchored), "0", result.Unanchored == 0),
		assert("no cue drifts beyond the tolerance", fmt.Sprint(result.Drifted), "0", result.Drifted == 0),
		assert("max alignment error is at most 1 ms", fmt.Sprint(result.MaxErrorNS), "<=1000000",
			result.MaxErrorNS <= synckernel.DefaultTolerance),
		assert("max alignment error matches a linear nearest-anchor scan", fmt.Sprint(result.MaxErrorNS),
			fmt.Sprint(expectedMax), result.MaxErrorNS == expectedMax),
		assert("alignment actually measured a nonzero sub-millisecond error", boolText(result.MaxErrorNS > 0 && result.MaxErrorNS < 1_000_000),
			"true", result.MaxErrorNS > 0 && result.MaxErrorNS < 1_000_000),
		assert("caption times were not moved by alignment", boolText(cueTimesUnchanged(cueRefs, parsed)),
			"true", cueTimesUnchanged(cueRefs, parsed)),
	}
	r.metrics.MaxAlignErr = result.MaxErrorNS
	r.record(Scenario{
		Name: "alignment",
		Detail: fmt.Sprintf("%d cues vs %d RTP frame anchors, max error %d ns (tolerance %d ns)",
			len(cueRefs), len(anchors), result.MaxErrorNS, aligner.Tolerance()),
		Checks: checks,
	})
	return nil
}

func cueTimesUnchanged(refs []synckernel.CueRef, parsed subtitle.Result) bool {
	if len(refs) != len(parsed.Cues) {
		return false
	}
	for i, ref := range refs {
		if ref.StartNS != parsed.Cues[i].Start || ref.EndNS != parsed.Cues[i].End {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------------- render

func init() { register("alignment", (*runner).runAlignment) }
