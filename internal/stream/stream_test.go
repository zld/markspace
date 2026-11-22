package stream

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"

	"mediaarchive/internal/diag"
	"mediaarchive/internal/fixtures"
)

func writeTempMP4(t *testing.T, spec fixtures.MP4Spec) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mp4")
	if err := fixtures.WriteFile(path, fixtures.BuildMP4(spec)); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnalyzeMP4SampleTableAndKeyframes(t *testing.T) {
	spec := fixtures.MP4Spec{Timescale: 90000}
	for i := 0; i < 120; i++ {
		spec.Samples = append(spec.Samples, fixtures.MP4Sample{Size: 512, Delta: 3003, IsSync: i%24 == 0})
	}
	a, err := AnalyzeMP4(writeTempMP4(t, spec))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Faults) != 0 {
		t.Fatalf("faults: %v", a.Faults)
	}
	if a.Video == nil || len(a.Video.Samples) != 120 {
		t.Fatalf("video samples = %+v", a.Video)
	}
	if got := a.Video.Samples[10].Offset; got == 0 {
		t.Fatal("chunk offset expansion failed")
	}
	gap := a.Video.MaxKeyframeGapMs()
	if gap < 799 || gap > 802 {
		t.Fatalf("keyframe gap = %v ms", gap)
	}
	if a.Video.SyncSamples[1] != 25 {
		t.Fatalf("sync sample list = %v", a.Video.SyncSamples[:3])
	}
}

func TestAnalyzeMP4TruncatedTail(t *testing.T) {
	spec := fixtures.MP4Spec{Timescale: 90000}
	for i := 0; i < 60; i++ {
		spec.Samples = append(spec.Samples, fixtures.MP4Sample{Size: 2048, Delta: 3000, IsSync: i%15 == 0})
	}
	data := fixtures.BuildMP4(spec)
	// Cut inside the last mdat chunk, which must surface as a truncated mdat.
	cut := data[:len(data)-int(2048*10)]
	dir := t.TempDir()
	path := filepath.Join(dir, "cut.mp4")
	if err := os.WriteFile(path, cut, 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := AnalyzeMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range a.Faults {
		if f.Code == diag.CodeMP4Truncated {
			found = true
		}
	}
	if !found {
		t.Fatalf("no truncation fault: %v", a.Faults)
	}
	if !a.Truncated {
		t.Fatal("Truncated flag not set")
	}
	segs := Classify(a.Faults, ClassifyOptions{Source: "clip.mp4"})
	if len(segs) != 1 || segs[0].Code != "mp4" {
		t.Fatalf("segments = %+v", segs)
	}
}

func rtpPackets(n int, baseSeq uint16, baseTS uint32, step uint32) []Packet {
	out := make([]Packet, n)
	for i := range out {
		out[i] = Packet{Seq: baseSeq + uint16(i), Timestamp: baseTS + uint32(i)*step, SSRC: 42, Offset: int64(i)}
	}
	return out
}

func TestRTPGapAndRecovery(t *testing.T) {
	pkts := rtpPackets(100, 100, 9000, 900)
	// Drop packets at index 20..24 (sequence gap 5) and duplicate index 50.
	var lossy []Packet
	for i, p := range pkts {
		if i >= 20 && i <= 24 {
			continue
		}
		lossy = append(lossy, p)
	}
	lossy = append(lossy, lossy[50]) // duplicate
	a := AnalyzePackets(lossy, RTPOptions{ClockRate: 90000})
	if a.Missing != 5 || a.MaxGap != 5 {
		t.Fatalf("missing=%d maxgap=%d", a.Missing, a.MaxGap)
	}
	if a.Duplicates != 1 {
		t.Fatalf("duplicates=%d", a.Duplicates)
	}
	if len(a.Faults) != 2 {
		t.Fatalf("faults=%v", a.Faults)
	}
	segs := Classify(a.Faults, ClassifyOptions{Source: "feed.rtp", ByteMerge: 4})
	if len(segs) != 2 || segs[0].Code != "rtp" {
		t.Fatalf("segments=%+v", segs)
	}
}

func TestRTPReorderIsOutOfOrderNotDuplicate(t *testing.T) {
	pkts := rtpPackets(50, 1, 0, 3000)
	pkts[10], pkts[11] = pkts[11], pkts[10]
	a := AnalyzePackets(pkts, RTPOptions{ClockRate: 90000})
	if a.Duplicates != 0 {
		t.Fatalf("swap counted as %d duplicates", a.Duplicates)
	}
	if a.OutOfOrder != 1 {
		t.Fatalf("out-of-order = %d", a.OutOfOrder)
	}
}

func TestRTPSequenceWrapIsNotGap(t *testing.T) {
	pkts := rtpPackets(40, 65520, 0, 3000)
	a := AnalyzePackets(pkts, RTPOptions{ClockRate: 90000})
	if a.Missing != 0 || len(a.Faults) != 0 {
		t.Fatalf("wrap misclassified: missing=%d faults=%v", a.Missing, a.Faults)
	}
}

func TestRTPTimestampWrapAndRollback(t *testing.T) {
	// Start close to 2^32 and cross it.
	var pkts []Packet
	for i := 0; i < 20; i++ {
		pkts = append(pkts, Packet{Seq: uint16(i), Timestamp: uint32(0xFFFFFF00 + uint32(i)*3600), SSRC: 7, Offset: int64(i)})
	}
	a := AnalyzePackets(pkts, RTPOptions{ClockRate: 90000})
	if a.TimestampJumps != 0 {
		t.Fatalf("wrap reported as jump: %v", a.Faults)
	}
	if a.DurationMs() <= 0 {
		t.Fatalf("duration across wrap = %v", a.DurationMs())
	}

	// A small backward step is a rollback fault with an offset.
	rb := []Packet{
		{Seq: 1, Timestamp: 100000, Offset: 0},
		{Seq: 2, Timestamp: 103000, Offset: 1},
		{Seq: 3, Timestamp: 102000, Offset: 2},
	}
	a2 := AnalyzePackets(rb, RTPOptions{ClockRate: 90000})
	f, ok := diag.As(a2.Faults[0])
	if !ok || f.Code != diag.CodeTimestampRollback || a2.Faults[0].Offset != 2 {
		t.Fatalf("rollback fault = %+v", a2.Faults)
	}
}

func TestRTPTimestampJump(t *testing.T) {
	pkts := []Packet{
		{Seq: 1, Timestamp: 0}, {Seq: 2, Timestamp: 3000},
		{Seq: 3, Timestamp: 3000 + 90000*10},
	}
	a := AnalyzePackets(pkts, RTPOptions{ClockRate: 90000})
	if a.TimestampJumps != 1 {
		t.Fatalf("jumps = %d faults=%v", a.TimestampJumps, a.Faults)
	}
}

func TestPionRTPHeaderDecodeMapsFields(t *testing.T) {
	p := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 777, Timestamp: 123456, SSRC: 99, PayloadType: 96, Marker: true}}
	pkt := FromRTP(p, 5)
	if pkt.Seq != 777 || pkt.Timestamp != 123456 || pkt.SSRC != 99 || pkt.Payload != 96 || !pkt.Marker || pkt.Offset != 5 {
		t.Fatalf("mapped packet = %+v", pkt)
	}
}

func TestClassifySegmentsDoNotOverlap(t *testing.T) {
	var faults []*diag.Fault
	for i := 0; i < 20; i++ {
		f := diag.New(diag.CodeRTPSegmentGap, int64(i*100), "gap %d", i)
		f.WithMedia(float64(i) * 10)
		faults = append(faults, f)
	}
	segs := Classify(faults, ClassifyOptions{Source: "x.rtp", TimeKnown: true, TimeMergeMs: 5, ByteMerge: 150})
	if overlaps, unsorted := Validate(segs); overlaps != 0 || unsorted {
		t.Fatalf("overlaps=%d unsorted=%v segs=%+v", overlaps, unsorted, segs)
	}
	if len(segs) == 0 {
		t.Fatal("no segments")
	}
}

func TestBoundedFaultMemory(t *testing.T) {
	opts := RTPOptions{ClockRate: 90000, MaxFaults: 8, DupWindow: 32}
	a := NewRTPAnalysis(opts)
	for i := 0; i < 10000; i++ {
		// Every other packet duplicates so faults are produced continuously.
		a.Push(Packet{Seq: uint16(i / 2), Timestamp: uint32(i / 2 * 900), Offset: int64(i)})
	}
	a.Finish()
	if len(a.Faults) > 8 {
		t.Fatalf("fault list grew to %d", len(a.Faults))
	}
	if a.Suppressed == 0 {
		t.Fatal("expected suppressed faults")
	}
	if len(a.seen) > 64 || len(a.holes) > 64 {
		t.Fatalf("windows unbounded: seen=%d holes=%d", len(a.seen), len(a.holes))
	}
}
