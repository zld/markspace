package inspect

import (
	"testing"

	"tsrebuild/internal/program"
	"tsrebuild/internal/psi"
	"tsrebuild/internal/tsreader"
)

func pkt(pid uint16, idx int64, cc uint8) *tsreader.Packet {
	return &tsreader.Packet{Index: idx, PID: pid, ContinuityCounter: cc, HasPayload: true}
}

func buildTimeline(t *testing.T) *program.Timeline {
	t.Helper()
	tl := program.NewTimeline(9)
	tl.ApplyPAT(&psi.PAT{TransportStreamID: 9, Version: 0, Programs: []psi.PATProgram{{ProgramNumber: 1, PMTPID: 100}}})
	pmt0 := &psi.PMT{ProgramNumber: 1, PCRPID: 256, Version: 0, Streams: []psi.ElementaryStream{
		{PID: 256, StreamType: 0x1b, Kind: psi.StreamVideo},
		{PID: 257, StreamType: 0x0f, Kind: psi.StreamAudio},
	}}
	if _, err := tl.ApplyMapping(1, 100, 0, 0, pmt0, 10); err != nil {
		t.Fatal(err)
	}
	tl.ApplyPAT(&psi.PAT{TransportStreamID: 9, Version: 1, Programs: []psi.PATProgram{{ProgramNumber: 1, PMTPID: 100}}})
	pmt1 := &psi.PMT{ProgramNumber: 1, PCRPID: 300, Version: 1, Streams: []psi.ElementaryStream{
		{PID: 300, StreamType: 0x1b, Kind: psi.StreamVideo},
		{PID: 257, StreamType: 0x0f, Kind: psi.StreamAudio},
	}}
	if _, err := tl.ApplyMapping(1, 100, 1, 1, pmt1, 40); err != nil {
		t.Fatal(err)
	}
	return tl
}

func TestCountersBasicContinuity(t *testing.T) {
	tl := buildTimeline(t)
	c := NewCollector(1, tl)

	// Wrap 14->15->0 must not be a gap; 3->5 is one gap; 6->6 one duplicate.
	for _, cc := range []uint8{14, 15, 0, 1, 2, 3, 5, 6, 6} {
		c.Observe(pkt(256, 10, cc))
	}
	res := c.Finalize()
	if len(res.Intervals) != 1 {
		t.Fatalf("expected one interval group, got %d", len(res.Intervals))
	}
	var got *PIDStats
	for i := range res.Intervals[0] {
		if res.Intervals[0][i].PID == 256 {
			got = &res.Intervals[0][i]
		}
	}
	if got == nil || got.Packets != 9 || got.ContinuityGaps != 1 || got.DuplicateCounters != 1 {
		t.Fatalf("continuity counters wrong: %+v", got)
	}
	if got.Kind != psi.StreamVideo {
		t.Fatalf("kind wrong: %s", got.Kind)
	}
}

func TestCountersTEIIgnoresCC(t *testing.T) {
	tl := buildTimeline(t)
	c := NewCollector(1, tl)
	c.Observe(pkt(256, 10, 1))
	bad := pkt(256, 11, 9)
	bad.TransportError = true
	c.Observe(bad)
	c.Observe(pkt(256, 12, 2)) // CC continues from 1, not from the corrupt packet
	res := c.Finalize()
	s := findStats(res, 256)
	if s == nil || s.TransportErrors != 1 || s.ContinuityGaps != 0 || s.Packets != 3 {
		t.Fatalf("TEI handling wrong: %+v", s)
	}
}

func TestCountersAdaptationOnlyDoesNotAdvanceCC(t *testing.T) {
	tl := buildTimeline(t)
	c := NewCollector(1, tl)
	c.Observe(pkt(256, 10, 4))
	af := pkt(256, 11, 4)
	af.HasPayload = false
	af.HasAdaptationField = true
	c.Observe(af)
	c.Observe(pkt(256, 12, 5))
	res := c.Finalize()
	s := findStats(res, 256)
	if s == nil || s.PayloadPackets != 2 || s.Packets != 3 || s.ContinuityGaps != 0 || s.DuplicateCounters != 0 {
		t.Fatalf("adaptation-only handling wrong: %+v", s)
	}
}

func TestCountersAttributionAcrossRemap(t *testing.T) {
	tl := buildTimeline(t)
	c := NewCollector(1, tl)

	c.Observe(pkt(0, 0, 0)) // PAT, global
	for i := int64(0); i < 10; i++ {
		c.Observe(pkt(256, 10+i, uint8(i))) // video on old PID, pre-switch
	}
	// Audio 257 spans the boundary; inject one CC jump after the switch.
	for i := int64(0); i < 5; i++ {
		c.Observe(pkt(257, 15+i, uint8(i)))
	}
	c.Observe(pkt(257, 40, 5))
	c.Observe(pkt(257, 41, 9)) // jump 5 -> 9: one gap
	for i := int64(0); i < 8; i++ {
		c.Observe(pkt(300, 42+i, uint8(i))) // video on new PID, post-switch
	}
	// A foreign program's packet must not be attributed.
	c.Observe(pkt(999, 50, 0))

	res := c.Finalize()
	if res.PAT.Packets != 1 {
		t.Fatalf("PAT stats wrong: %+v", res.PAT)
	}
	if len(res.Intervals) != 2 {
		t.Fatalf("expected two interval groups, got %d", len(res.Intervals))
	}

	pre := res.Intervals[0]
	post := res.Intervals[1]
	if pre[0].IntervalStart != 10 || post[0].IntervalStart != 40 {
		t.Fatalf("interval grouping wrong: %+v %+v", pre, post)
	}
	if findInGroup(pre, 256) == nil || findInGroup(pre, 300) != nil {
		t.Fatal("PID 256 must only appear pre-switch, 300 must not")
	}
	if findInGroup(post, 300) == nil || findInGroup(post, 256) != nil {
		t.Fatal("PID 300 must only appear post-switch, 256 must not")
	}
	if s := findInGroup(pre, 256); s.Packets != 10 {
		t.Fatalf("old video packet count wrong: %+v", s)
	}
	if s := findInGroup(post, 300); s.Packets != 8 {
		t.Fatalf("new video packet count wrong: %+v", s)
	}
	var audioTotal, audioGaps int64
	for _, g := range [][]PIDStats{pre, post} {
		if s := findInGroup(g, 257); s != nil {
			audioTotal += s.Packets
			audioGaps += s.ContinuityGaps
		}
	}
	if audioTotal != 7 || audioGaps != 1 {
		t.Fatalf("audio attribution wrong: packets=%d gaps=%d", audioTotal, audioGaps)
	}
}

func TestCountersLossOnOnePIDKeepsOthers(t *testing.T) {
	tl := buildTimeline(t)
	c := NewCollector(1, tl)
	c.Observe(pkt(256, 10, 0))
	c.Observe(pkt(257, 10, 0))
	c.Observe(pkt(256, 11, 5)) // gap on video only
	c.Observe(pkt(257, 11, 1)) // audio continuity intact
	res := c.Finalize()
	if findStats(res, 256).ContinuityGaps != 1 || findStats(res, 257).ContinuityGaps != 0 {
		t.Fatalf("per-PID isolation broken: %+v %+v", findStats(res, 256), findStats(res, 257))
	}
}

func findStats(r Result, pid uint16) *PIDStats {
	for _, g := range r.Intervals {
		if s := findInGroup(g, pid); s != nil {
			return s
		}
	}
	return nil
}

func findInGroup(g []PIDStats, pid uint16) *PIDStats {
	for i := range g {
		if g[i].PID == pid {
			return &g[i]
		}
	}
	return nil
}
