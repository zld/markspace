package clock

import (
	"testing"

	"github.com/pion/rtp"
)

func TestUnwrapperHandlesWraparound(t *testing.T) {
	u := NewUnwrapper(90000)
	// Start near the top of the 32-bit range.
	first, ev, err := u.Observe(0xFFFFFFF0)
	if err != nil || first != 0xFFFFFFF0 || ev.Wrapped {
		t.Fatalf("first observe: v=%d ev=%+v err=%v", first, ev, err)
	}
	// Wrap upwards by 32 ticks.
	second, ev, err := u.Observe(0x00000010)
	if err != nil {
		t.Fatal(err)
	}
	want := uint64(0x100000010)
	if second != want || !ev.Wrapped || u.Wraps() != 1 {
		t.Fatalf("wrap: got %d want %d ev=%+v wraps=%d", second, want, ev, u.Wraps())
	}
	if ev.Delta != 32 {
		t.Fatalf("wrap delta=%d want 32", ev.Delta)
	}
}

func TestUnwrapperDistinguishesRollback(t *testing.T) {
	u := NewUnwrapper(90000)
	_, _, _ = u.Observe(100000)
	// Small backwards jump is a real rollback, not a wrap.
	v, ev, err := u.Observe(99900)
	if err != nil {
		t.Fatal(err)
	}
	if v != 99900 || !ev.Rollback || ev.Wrapped || u.Rollbacks() != 1 {
		t.Fatalf("rollback: v=%d ev=%+v rollbacks=%d", v, ev, u.Rollbacks())
	}
	if ev.Delta != -100 {
		t.Fatalf("rollback delta=%d want -100", ev.Delta)
	}
}

func TestUnwrapperMonotonicForward(t *testing.T) {
	u := NewUnwrapper(48000)
	prev := uint64(0)
	for _, ts := range []uint32{100, 580, 1060, 1540} {
		v, ev, err := u.Observe(ts)
		if err != nil || ev.Wrapped || ev.Rollback {
			t.Fatalf("unexpected event at %d: ev=%+v err=%v", ts, ev, err)
		}
		if v < prev {
			t.Fatalf("extended time went backwards: %d < %d", v, prev)
		}
		prev = v
	}
}

func TestAnchorMapping(t *testing.T) {
	// 90 kHz RTP clock, anchor at RTP 90000 == shared 1000 ms, media==RTP.
	a := ClockAnchor{SSRC: 1, ClockHz: 90000, AnchorRTP: 90000, AnchorSharedMs: 1000, MediaToRTP: 0}
	// Media time 180000 (2 s) -> RTP 180000 -> 1000 ms later -> 2000 ms.
	got, err := a.MediaToShared(180000)
	if err != nil || got != 2000 {
		t.Fatalf("MediaToShared=%d err=%v want 2000", got, err)
	}
	// Direct RTP mapping across a wrap boundary: one full 2^32 cycle at
	// 90 kHz is 47721858.84... ms, rounded to 47721859, plus the 1000 ms.
	got, err = a.RTPToShared(0x100000000 + 90000)
	if err != nil || got != 1000+47721859 {
		t.Fatalf("RTPToShared after wrap=%d err=%v", got, err)
	}
}

func TestCalibrateIsolatesDivergentRepresentation(t *testing.T) {
	c := NewCalibrator()
	// Two renditions aligned at the anchor; the third is skewed by 5 ms.
	_ = c.SetAnchor("v0", ClockAnchor{ClockHz: 90000, AnchorRTP: 0, AnchorSharedMs: 0})
	_ = c.SetAnchor("v1", ClockAnchor{ClockHz: 48000, AnchorRTP: 0, AnchorSharedMs: 0})
	_ = c.SetAnchor("v2", ClockAnchor{ClockHz: 90000, AnchorRTP: 0, AnchorSharedMs: 0})
	bounds := []Boundary{
		{Representation: "v0", Sequence: 4, MediaTime: 900000}, // 10000 ms
		{Representation: "v1", Sequence: 4, MediaTime: 480000}, // 10000 ms
		{Representation: "v2", Sequence: 4, MediaTime: 900450}, // 10005 ms
	}
	ref, divs, err := c.Calibrate(bounds)
	if err != nil {
		t.Fatal(err)
	}
	if ref != 10000 || len(divs) != 1 {
		t.Fatalf("ref=%d divs=%+v", ref, divs)
	}
	if divs[0].Representation != "v2" || divs[0].DeltaMs != 5 {
		t.Fatalf("wrong divergence: %+v", divs[0])
	}
	if !bounds[2].Isolated || bounds[0].Isolated {
		t.Fatalf("isolation flags wrong: %+v", bounds)
	}
}

func TestCalibrateWithinOneTickPasses(t *testing.T) {
	c := NewCalibrator()
	_ = c.SetAnchor("v0", ClockAnchor{ClockHz: 90000, AnchorRTP: 0, AnchorSharedMs: 0})
	_ = c.SetAnchor("v1", ClockAnchor{ClockHz: 90000, AnchorRTP: 0, AnchorSharedMs: 0})
	// 10000 ms vs 10000 ms after rounding (sub-ms source difference).
	bounds := []Boundary{
		{Representation: "v0", Sequence: 1, MediaTime: 900000},
		{Representation: "v1", Sequence: 1, MediaTime: 900000},
	}
	if _, divs, err := c.Calibrate(bounds); err != nil || len(divs) != 0 {
		t.Fatalf("aligned boundaries must pass: divs=%+v err=%v", divs, err)
	}
}

func TestCalibrateMissingAnchorIsolatesOnlyThatRep(t *testing.T) {
	c := NewCalibrator()
	_ = c.SetAnchor("v0", ClockAnchor{ClockHz: 90000, AnchorRTP: 0, AnchorSharedMs: 0})
	bounds := []Boundary{
		{Representation: "v0", Sequence: 2, MediaTime: 90000},
		{Representation: "ghost", Sequence: 2, MediaTime: 90000},
	}
	ref, divs, err := c.Calibrate(bounds)
	if err != nil {
		t.Fatal(err)
	}
	if ref != 1000 || len(divs) != 1 || divs[0].Representation != "ghost" {
		t.Fatalf("ref=%d divs=%+v", ref, divs)
	}
	if !bounds[1].Isolated || bounds[0].Isolated {
		t.Fatalf("only the anchorless rep must be isolated")
	}
}

func TestParsePacketWithPion(t *testing.T) {
	src := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: 0x1234,
			Timestamp:      0xFFFFFFF0,
			SSRC:           0xDEADBEEF,
			Marker:         true,
		},
		Payload: []byte{1, 2, 3, 4},
	}
	raw, err := src.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := ParsePacket(raw)
	if err != nil {
		t.Fatalf("ParsePacket: %v", err)
	}
	if pkt.Timestamp != 0xFFFFFFF0 || pkt.SSRC != 0xDEADBEEF || len(pkt.Payload) != 4 {
		t.Fatalf("decoded packet wrong: %+v", pkt)
	}
	if _, err := ParsePacket([]byte{0x80}); err == nil {
		t.Fatal("truncated packet must fail parsing")
	}
}

func TestZeroClockRateRejected(t *testing.T) {
	u := NewUnwrapper(0)
	if _, _, err := u.Observe(1); err == nil {
		t.Fatal("zero-rate unwrapper must reject observations")
	}
	a := ClockAnchor{ClockHz: 0}
	if _, err := a.MediaToShared(1); err == nil {
		t.Fatal("zero-rate anchor must reject mapping")
	}
}
