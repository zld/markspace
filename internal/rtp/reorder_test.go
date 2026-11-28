package rtp_test

import (
	"testing"

	"rtptrace/internal/container"
	"rtptrace/internal/fixture"
	"rtptrace/internal/rtp"
)

func mkPkt(ssrc uint32, seq uint16) *rtp.Packet {
	p := &rtp.Packet{SSRC: ssrc, SequenceNumber: seq, PayloadType: 96,
		Payload: []byte{0xAB, 0xCD}}
	if err := rtp.Encode(p); err != nil {
		panic(err)
	}
	return p
}

func TestDedup(t *testing.T) {
	w, _ := rtp.NewWindow(7, 8)
	seqs := []uint16{1, 2, 2, 3, 1}
	var out []*rtp.Packet
	for _, s := range seqs {
		rel, err := w.Push(mkPkt(7, s))
		if err != nil {
			t.Fatalf("push %d: %v", s, err)
		}
		out = append(out, rel...)
	}
	got := []uint16{}
	for _, p := range out {
		got = append(got, p.SequenceNumber)
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("in-order output wrong: %v", got)
	}
	st, _ := w.Flush()
	if st.Duplicates != 2 || st.Received != 3 {
		t.Fatalf("dup=%d received=%d want 2/3", st.Duplicates, st.Received)
	}
}

func TestReorderWithinWindow(t *testing.T) {
	w, _ := rtp.NewWindow(1, 8)
	// Deliver 1,3,2,4: 3 is buffered, 2 releases 1,2,3,4.
	var out []*rtp.Packet
	for _, s := range []uint16{1, 3, 2, 4} {
		rel, err := w.Push(mkPkt(1, s))
		if err != nil {
			t.Fatalf("push: %v", err)
		}
		out = append(out, rel...)
	}
	if len(out) != 4 {
		t.Fatalf("emitted=%d want 4", len(out))
	}
	for i, p := range out {
		if p.SequenceNumber != uint16(i+1) {
			t.Fatalf("position %d got seq %d", i, p.SequenceNumber)
		}
	}
	st, gaps := w.Flush()
	if st.Reordered == 0 || st.LostPackets != 0 || len(gaps) != 0 {
		t.Fatalf("windowed reorder should not lose packets: %+v gaps=%v", st, gaps)
	}
}

func TestBurstLossOutsideWindow(t *testing.T) {
	w, _ := rtp.NewWindow(9, 4)
	// 0,1 then jump to 10 (gap 2..9, distance 10 > capacity 4).
	for _, s := range []uint16{0, 1, 10, 11} {
		if _, err := w.Push(mkPkt(9, s)); err != nil {
			t.Fatalf("push: %v", err)
		}
	}
	st, gaps := w.Flush()
	if st.LostPackets != 8 {
		t.Fatalf("lost=%d want 8", st.LostPackets)
	}
	if len(gaps) != 1 || gaps[0].Start != 2 || gaps[0].Count != 8 {
		t.Fatalf("gap wrong: %+v", gaps)
	}
}

func TestSequenceWrap(t *testing.T) {
	w, _ := rtp.NewWindow(5, 16)
	for _, s := range []uint16{65534, 65535, 0, 1} {
		if _, err := w.Push(mkPkt(5, s)); err != nil {
			t.Fatalf("push: %v", err)
		}
	}
	out := w.Drain()
	want := []uint16{65534, 65535, 0, 1}
	for i, p := range out {
		if p.SequenceNumber != want[i] {
			t.Fatalf("wrap position %d got %d want %d", i, p.SequenceNumber, want[i])
		}
	}
}

func TestBandwidthOnlyChangesTiming(t *testing.T) {
	pkts := []*rtp.Packet{mkPkt(3, 0), mkPkt(3, 1), mkPkt(3, 2)}
	mediaTS := make([]uint32, len(pkts))
	for i, p := range pkts {
		mediaTS[i] = p.Timestamp
	}
	plan := []rtp.BandwidthPoint{
		{StartUS: 0, BitsPerSec: 1_000_000},
		{StartUS: 100, BitsPerSec: 100_000},
	}
	sched, err := rtp.NewScheduler(plan)
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}
	if err := sched.Schedule(pkts, func(p *rtp.Packet) int { return len(p.Raw) }); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if pkts[0].SendTimeUS == 0 || !(pkts[2].SendTimeUS > pkts[0].SendTimeUS) {
		t.Fatal("send times should advance under shaping")
	}
	for i, p := range pkts {
		if p.Timestamp != mediaTS[i] {
			t.Fatalf("media timestamp changed by bandwidth at %d", i)
		}
	}
	// Monotonic non-decreasing send times.
	for i := 1; i < len(pkts); i++ {
		if pkts[i].SendTimeUS < pkts[i-1].SendTimeUS {
			t.Fatal("send times must be monotonic")
		}
	}
}

func TestBuildPacketsUsesMediaTimeBase(t *testing.T) {
	dir := t.TempDir()
	f, err := fixture.BuildMP4(dir, 17, []fixture.TrackSpec{
		{TrackID: 1, Timescale: 48000, SampleCount: 8, SampleSize: 16,
			SamplesPerChunk: 4, Delta: 480, SSRC: 111, PayloadType: 96},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	c, err := container.ScanFile(f.Path)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	pkts, err := rtp.BuildPackets(c)
	if err != nil {
		t.Fatalf("build packets: %v", err)
	}
	if len(pkts) != 8 {
		t.Fatalf("packets=%d want 8", len(pkts))
	}
	// Sample i dts = i*480 at 48kHz => i*1000us => 90kHz RTP ts = i*900.
	for i, p := range pkts {
		if p.Timestamp != uint32(i*900) {
			t.Fatalf("pkt %d rtp ts=%d want %d", i, p.Timestamp, i*900)
		}
		if len(p.Raw) == 0 {
			t.Fatal("raw encoding missing")
		}
	}
}

func TestReassembleEndToEnd(t *testing.T) {
	dir := t.TempDir()
	f, _ := fixture.BuildMP4(dir, 17, []fixture.TrackSpec{
		{TrackID: 1, Timescale: 90000, SampleCount: 20, SampleSize: 12,
			SamplesPerChunk: 5, Delta: 90, SSRC: 5, PayloadType: 96},
	})
	c, err := container.ScanFile(f.Path)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	ordered, err := rtp.BuildPackets(c)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	cfg := rtp.TraceConfig{
		WindowCapacity: 4, ReorderEvery: 3, DuplicateEvery: 5,
		BurstLoss: []int{12, 13, 14}, StartSeq: 100,
	}
	delivered := rtp.Simulate(ordered, cfg, 17)
	emitted, stats, gaps, err := rtp.Reassemble(delivered, cfg.WindowCapacity)
	if err != nil {
		t.Fatalf("reassemble: %v", err)
	}
	if stats.Duplicates == 0 {
		t.Fatal("expected duplicate deliveries")
	}
	if stats.LostPackets < 3 {
		t.Fatalf("lost=%d expected at least the 3 burst packets", stats.LostPackets)
	}
	if len(emitted) == 0 {
		t.Fatal("no packets emitted")
	}
	if len(gaps) == 0 {
		t.Fatal("burst loss must produce gaps")
	}
}
