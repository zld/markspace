package fanout_test

import (
	"bytes"
	"testing"

	"mediapack/internal/fanout"
	"mediapack/internal/mp4idx"
)

func frag(rate int, id string, start, end int64, startTick uint64, kf []int64) *fanout.ParsedFragment {
	return &fanout.ParsedFragment{
		Header: fanout.Header{
			ID: id, RateIndex: rate, SourceStart: start, SourceEnd: end,
			StartTick: startTick, EndTick: startTick + 18000,
		},
		MP4: &mp4idx.IndexPayload{KeyframeOff: kf, StartTick: startTick},
	}
}

func TestFanInAlignedRepresentations(t *testing.T) {
	kf := []int64{4512}
	fs := []*fanout.ParsedFragment{
		frag(0, "seg_0001", 4512, 9024, 8589916592, kf),
		frag(1, "seg_0001", 4512, 9024, 8589916592, kf),
	}
	rep := fanout.FanIn(fs)
	if !rep.Aligned || rep.MaxErrorPackets != 0 || len(rep.IsolatedArtifact) != 0 {
		t.Fatalf("expected aligned fan-in: %+v", rep)
	}
}

func TestFanInToleratesOnePacketBoundary(t *testing.T) {
	kf := []int64{0}
	fs := []*fanout.ParsedFragment{
		frag(0, "seg_0000", 0, 4512, 100, kf),
		frag(1, "seg_0000", 188, 4700, 100, kf), // exactly one TS packet shift
	}
	rep := fanout.FanIn(fs)
	if !rep.Aligned || rep.MaxErrorPackets != 1 {
		t.Fatalf("one-packet deviation must be tolerated: aligned=%v err=%d", rep.Aligned, rep.MaxErrorPackets)
	}
}

func TestFanInIsolatesMismatchedRepresentation(t *testing.T) {
	kf := []int64{9024}
	fs := []*fanout.ParsedFragment{
		frag(0, "seg_0002", 9024, 13536, 8589934592, kf),
		frag(1, "seg_0002", 9024, 13536, 8589934592, kf),
		frag(2, "seg_0002", 9400, 13536, 8589934592, kf), // 2 packets late start
	}
	rep := fanout.FanIn(fs)
	if rep.Aligned {
		t.Fatal("2-packet boundary mismatch must make fan-in unaligned")
	}
	if len(rep.IsolatedArtifact) != 1 || rep.IsolatedArtifact[0] != "r2/seg_0002" {
		t.Fatalf("isolated=%v want [r2/seg_0002]", rep.IsolatedArtifact)
	}

	// A tick mismatch must also isolate even if offsets agree.
	bad := frag(3, "seg_0002", 9024, 13536, 123, kf)
	rep2 := fanout.FanIn(append(fs[:2], bad))
	if len(rep2.IsolatedArtifact) != 1 || rep2.IsolatedArtifact[0] != "r3/seg_0002" {
		t.Fatalf("tick mismatch not isolated: %+v", rep2.IsolatedArtifact)
	}
}

func TestBuildParseAndCheckFragment(t *testing.T) {
	src := bytes.NewReader(make([]byte, 24*188))
	h := fanout.Header{ID: "seg_0000", IntervalIndex: 0, RateIndex: 0, RateName: "r0",
		SourceStart: 0, SourceEnd: 24 * 188, StartTick: 100, EndTick: 18100, StructuralDigest: "d"}
	data, err := fanout.BuildFragment(fanout.BuildInput{
		Header: h, Keyframes: []int64{0}, Source: src, SourceStart: 0, SourceEnd: 24 * 188,
	})
	if err != nil {
		t.Fatal(err)
	}
	pf, err := fanout.ParseFragment(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pf.Header.ID != "seg_0000" || len(pf.TS) != 24*188 || pf.MP4.StartTick != 100 {
		t.Fatalf("fragment roundtrip wrong: %+v", pf.Header)
	}
	res, err := fanout.Check(data)
	if err != nil || !res.TSAligned || !res.MP4OK || res.RTP.TSPackets != 24 {
		t.Fatalf("check res=%+v err=%v", res, err)
	}
}

func TestProfilesAndRelPath(t *testing.T) {
	ps := fanout.Profiles(3)
	if len(ps) != 3 || ps[0].Name != "r0" || ps[2].BitrateKbps <= ps[0].BitrateKbps {
		t.Fatalf("profiles wrong: %+v", ps)
	}
	if fanout.RelPath(1, "seg_0002") != "segments/r1/seg_0002.frag" {
		t.Fatal("rel path mismatch")
	}
	if fanout.ArtifactID(1, "seg_0002") != "r1/seg_0002" {
		t.Fatal("artifact id mismatch")
	}
}
