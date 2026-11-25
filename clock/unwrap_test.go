package clock

import (
	"testing"
	"time"

	"rtpreplay/media"
)

var t0 = time.Unix(0, 0).UTC()

func feed(t *testing.T, u *Unwrapper, seqTS ...uint32) []*Packet {
	t.Helper()
	var out []*Packet
	for i := 0; i < len(seqTS); i += 2 {
		p, err := u.Add(i/2, seqTS[i], seqTS[i+1], t0.Add(time.Duration(i/2)*time.Millisecond*20))
		if err != nil {
			t.Fatalf("add %d: %v", i/2, err)
		}
		out = append(out, p)
	}
	u.Finish()
	return out
}

func TestMonotonicAndSeqGap(t *testing.T) {
	u, _ := New(media.VideoClockRate)
	pk := feed(t, u, 0, 0, 1, 3000, 4, 12000) // gap: seq 2,3 missing
	if pk[0].Unwrapped != 0 || pk[1].Unwrapped != 3000 {
		t.Fatal("basic unwrap wrong")
	}
	if pk[2].Anomaly != AnomSeqGap || pk[2].Isolated {
		t.Fatalf("gap must be flagged but not isolated: %+v", pk[2])
	}
	if len(u.IsolatedRanges()) != 0 {
		t.Fatal("healthy stream must have no isolated ranges")
	}
}

func TestTimestampWrapUnderContinuity(t *testing.T) {
	u, _ := New(media.VideoClockRate)
	// Two consecutive 3000-tick steps; the second crosses the 32-bit boundary.
	base := uint32(0xFFFFFFFF) - 3000 // ...CFF, then ...FBFF, then 0x00000BFF
	pk := feed(t, u, 10, base, 11, base+3000, 12, base+6000, 13, base+9000)
	if pk[2].Anomaly != AnomWrap {
		t.Fatalf("want wrap, got %s", pk[2].Anomaly)
	}
	if pk[2].Isolated {
		t.Fatal("continuity-held wrap must not isolate")
	}
	// The monotonic accumulator adds the small modular step; the 32-bit
	// crossing is recorded in WrapCount rather than re-added as 2^32.
	want := int64(base) + 6000
	if pk[2].Unwrapped != want {
		t.Fatalf("unwrapped=%d want %d", pk[2].Unwrapped, want)
	}
	if u.WrapCount() != 1 || len(u.IsolatedRanges()) != 0 {
		t.Fatal("wrap count/ranges wrong")
	}
}

func TestBackwardIsIsolated(t *testing.T) {
	u, _ := New(media.VideoClockRate)
	// A backwards step larger than the plausibility window cannot be a
	// rollover (whose true forward step would be near 2^32 ticks), so it is
	// isolated as a genuine backwards jump.
	back := uint32(11 * 90000)
	pk := feed(t, u, 0, 200000, 1, 200000-back, 2, 200000-back+3000)
	if pk[1].Anomaly != AnomBackward || !pk[1].Isolated {
		t.Fatalf("backward must be isolated: %+v", pk[1])
	}
	if pk[2].Isolated || pk[2].Anomaly != AnomNone {
		t.Fatalf("recovery packet must re-enter healthy region: %+v", pk[2])
	}
	r := u.IsolatedRanges()
	if len(r) != 1 || r[0].StartRecord != 1 || r[0].EndRecord != 1 || r[0].Reason != AnomBackward {
		t.Fatalf("ranges wrong: %+v", r)
	}
}

func TestJumpIsIsolatedAndRangeExtends(t *testing.T) {
	u, _ := New(media.AudioClockRate)
	// 11 s jump exceeds the 10 s window; two consecutive jumps share a range.
	pk := feed(t, u, 0, 0, 1, uint32(11*8000), 2, uint32(22*8000), 3, uint32(22*8000+160))
	if pk[1].Anomaly != AnomJump || !pk[1].Isolated {
		t.Fatalf("jump not isolated: %+v", pk[1])
	}
	if pk[2].RegionID != pk[1].RegionID {
		t.Fatal("consecutive anomalies must extend one region")
	}
	if pk[3].Isolated {
		t.Fatal("healthy packet must leave the isolated region")
	}
	r := u.IsolatedRanges()
	if len(r) != 1 || r[0].StartRecord != 1 || r[0].EndRecord != 2 {
		t.Fatalf("range wrong: %+v", r)
	}
}

func TestDuplicatePacket(t *testing.T) {
	u, _ := New(media.AudioClockRate)
	pk := feed(t, u, 5, 1600, 5, 1600, 6, 3200)
	if pk[1].Anomaly != AnomDuplicate {
		t.Fatalf("want duplicate, got %s", pk[1].Anomaly)
	}
	if pk[1].ExtSeq != pk[0].ExtSeq {
		t.Fatal("duplicate must keep the same extended sequence")
	}
	if pk[2].Anomaly != AnomNone {
		t.Fatal("stream must continue normally after a duplicate")
	}
}

func TestSequenceRestart(t *testing.T) {
	u, _ := New(media.AudioClockRate)
	pk := feed(t, u, 0, 0, 1, 160, 4000, 0, 4001, 160)
	if pk[2].Anomaly != AnomSeqRestart || !pk[2].Isolated {
		t.Fatalf("restart must isolate: %+v", pk[2])
	}
	if pk[3].Isolated {
		t.Fatal("post-restart healthy packet must leave isolation")
	}
	r := u.IsolatedRanges()
	if len(r) != 1 || r[0].StartRecord != 2 {
		t.Fatalf("range wrong: %+v", r)
	}
}

func TestMisorderedLatePacket(t *testing.T) {
	u, _ := New(media.AudioClockRate)
	pk := feed(t, u, 100, 16000, 101, 16160, 99, 15840, 102, 16320)
	if pk[2].Anomaly != AnomBackward || !pk[2].Isolated {
		t.Fatalf("late packet must be isolated: %+v", pk[2])
	}
	if pk[3].Anomaly != AnomNone || pk[3].Isolated {
		t.Fatal("stream must resume after the late packet")
	}
}

func TestUnsupportedRateRejected(t *testing.T) {
	if _, err := New(44100); err == nil {
		t.Fatal("unsupported rate must error")
	}
}
