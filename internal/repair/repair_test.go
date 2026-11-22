package repair

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"

	"mediacore/internal/clock"
)

func TestWrapExtension(t *testing.T) {
	eng, err := NewEngine(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	// Two packets straddling the 32-bit boundary at 90 kHz.
	pts := []struct {
		seq uint16
		ts  uint32
	}{
		{0, 0xFFFFFFF0},
		{1, 0x00000040},
	}
	var last int64
	for i, p := range pts {
		out, err := eng.Push(p.seq, p.ts, 100)
		if err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
		last = out[len(out)-1].Timestamp
	}
	if last != int64(0x100000040) {
		t.Fatalf("extended ts after wrap = %#x", last)
	}
	st := eng.Stats()
	if st.WrapCount != 1 || st.PacketsEmitted != 2 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestSequenceGap(t *testing.T) {
	eng, _ := NewEngine(DefaultConfig())
	// seq 10 then seq 13 -> two missing packets.
	if _, err := eng.Push(10, 1000, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Push(13, 3000, 1); err != nil {
		t.Fatal(err)
	}
	if got := eng.Stats().GapPackets; got != 2 {
		t.Fatalf("gap = %d, want 2", got)
	}
}

func TestSmallReorder(t *testing.T) {
	eng, _ := NewEngine(DefaultConfig())
	eng.Push(5, 1000, 1)
	eng.Push(6, 2000, 1)
	// Late packet inside the window with slightly earlier (but small) TS.
	out, err := eng.Push(7, 1990, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].RawTimestamp != 1990 {
		t.Fatalf("unexpected output %+v", out)
	}
	if eng.Stats().Reordered == 0 {
		t.Fatal("reorder not counted")
	}
}

func TestBigRegressionRejected(t *testing.T) {
	eng, _ := NewEngine(DefaultConfig())
	eng.Push(1, 90000*100, 1)
	_, err := eng.Push(2, 1000, 1) // ~100 s backwards at 90 kHz
	if clock.Classify(err) != clock.KindRecoverable {
		t.Fatalf("want recoverable error, got %v", err)
	}
	if eng.Stats().BigRegression != 1 {
		t.Fatal("big regression not counted")
	}
}

func TestSidecarRoundTripAndDeterminism(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rtp.bin")
	pkts := []rtp.Header{
		{Version: 2, PayloadType: 96, SequenceNumber: 0, Timestamp: 1000, SSRC: 7},
		{Version: 2, PayloadType: 96, SequenceNumber: 1, Timestamp: 2000, SSRC: 7},
		{Version: 2, PayloadType: 96, SequenceNumber: 2, Timestamp: 3000, SSRC: 7},
	}
	wire := []uint32{1200, 1400, 1100}
	if err := WriteSidecar(path, SidecarHeader{TrackID: 1, ClockRate: 90000, PacketCount: 3}, pkts, wire); err != nil {
		t.Fatal(err)
	}
	r1, err := RunFile(context.Background(), path, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	r2, err := RunFile(context.Background(), path, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if r1.Digest != r2.Digest || r1.Points != 3 {
		t.Fatalf("digest determinism broken: %s vs %s", r1.Digest, r2.Digest)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != sidecarHeaderSize+3*recordSize {
		t.Fatalf("sidecar size wrong: %v", err)
	}
}

func TestTruncatedSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rtp.bin")
	pkts := []rtp.Header{{Version: 2, SequenceNumber: 0, Timestamp: 1}}
	if err := WriteSidecar(path, SidecarHeader{TrackID: 1, ClockRate: 90000, PacketCount: 1}, pkts, []uint32{10}); err != nil {
		t.Fatal(err)
	}
	// Rewrite the header to claim 2 packets while only one record exists.
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0, 0, 0, 2}, 16); err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, err = RunFile(context.Background(), path, DefaultConfig())
	if clock.Classify(err) != clock.KindRecoverable {
		t.Fatalf("want recoverable, got %v", err)
	}
}
