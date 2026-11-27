package fixture

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/ingest"
	"mediad/internal/model"
)

func parseFirst(t *testing.T, spec ingest.TrackSpec, datagrams [][]byte) *ingest.AudioPacket {
	t.Helper()
	pkt, err := ingest.ParseRTP(spec, datagrams[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := ingest.Parse(spec, pkt); err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	return pkt
}

func TestHealthyFixtureRoundTrip(t *testing.T) {
	pkts := OpusTrack()
	if len(pkts) != FrameCount {
		t.Fatalf("frame count = %d", len(pkts))
	}
	dgrams, err := SplitDatagrams(joinRaw(pkts))
	if err != nil {
		t.Fatal(err)
	}
	if len(dgrams) != FrameCount {
		t.Fatalf("datagrams = %d", len(dgrams))
	}
	spec := Specs()[0]
	p0 := parseFirst(t, spec, dgrams)
	if p0.DurationNS != 20*model.Millisecond || p0.Sequence != 0 {
		t.Fatalf("first packet = %+v", p0)
	}
	// Total healthy duration: 50 * 20ms = 1s.
	if int64(FrameCount)*int64(20*model.Millisecond) != int64(model.Second) {
		t.Fatal("fixture duration constant drifted")
	}
}

func TestDuplicateFixtureReportsOneExtraNoDuration(t *testing.T) {
	dup := WithDuplicate()
	if len(dup) != FrameCount+1 {
		t.Fatalf("duplicate fixture length = %d want %d", len(dup), FrameCount+1)
	}
	// Exactly one sequence number appears twice.
	counts := map[uint16]int{}
	for _, p := range dup {
		counts[p.Sequence]++
	}
	repeat := 0
	for _, c := range counts {
		if c == 2 {
			repeat++
		}
	}
	if repeat != 1 {
		t.Fatalf("want exactly 1 repeated sequence, got %d", repeat)
	}
}

func TestMissingFixtureHasHole(t *testing.T) {
	miss := WithMissing()
	if len(miss) != FrameCount-1 {
		t.Fatalf("missing fixture length = %d", len(miss))
	}
	seqs := map[uint16]bool{}
	for _, p := range miss {
		seqs[p.Sequence] = true
	}
	if seqs[20] {
		t.Fatal("seq 20 must be absent")
	}
}

func TestDamagedFixturesAreRejected(t *testing.T) {
	opusSpec := Specs()[0]
	bad, err := SplitDatagrams(joinRaw([]Packet{BadOpusTOCPacket()}))
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := ingest.ParseRTP(opusSpec, bad[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if model.AsError(ingest.Parse(opusSpec, pkt)).Code != model.ErrOpusTOC {
		t.Fatal("bad Opus TOC must be rejected")
	}
	aacSpec := Specs()[1]
	trunc, err := SplitDatagrams(joinRaw([]Packet{TruncatedAACPacket()}))
	if err != nil {
		t.Fatal(err)
	}
	pkt2, err := ingest.ParseRTP(aacSpec, trunc[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if model.AsError(ingest.Parse(aacSpec, pkt2)).Code != model.ErrAACHeader {
		t.Fatal("truncated AAC must be rejected")
	}
}

func TestMP4FixtureReadBack(t *testing.T) {
	raw := MP4TimeBase(48000, FrameCount, 960, 97)
	tb, err := ingest.ReadMP4TimeBase(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if tb.Timescale != 48000 || len(tb.SampleDeltas) != FrameCount {
		t.Fatalf("time base = %+v", tb)
	}
	if tb.SampleSizes[0] != 97 || tb.DurationTicks != uint64(FrameCount)*960 {
		t.Fatalf("sample boxes wrong: %+v", tb)
	}
}

func TestWriteAllCreatesFiles(t *testing.T) {
	dir := t.TempDir()
	out, err := WriteAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{ScenHealthy + ".rtp", ScenDuplicate + ".rtp", ScenMissing + ".rtp",
		ScenBadOpus + ".rtp", ScenTruncAAC + ".rtp", ScenMP4 + ".mp4"}
	for _, n := range names {
		info, err := os.Stat(filepath.Join(out, n))
		if err != nil || info.Size() == 0 {
			t.Fatalf("fixture %s missing or empty: %v", n, err)
		}
	}
}

func TestSplitRejectsBadFraming(t *testing.T) {
	if _, err := SplitDatagrams([]byte("XXXX")); model.AsError(err).Code != model.ErrRTPTruncated {
		t.Fatalf("got %v", err)
	}
}
