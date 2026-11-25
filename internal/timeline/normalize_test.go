package timeline

import (
	"testing"

	"github.com/pion/rtp"
)

func pkt(ts uint32, seq uint16, marker bool) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{Timestamp: ts, SequenceNumber: seq, Marker: marker},
	}
}

func TestExpandTimestampWrap(t *testing.T) {
	prev := uint64(0xFFFFFFF0)
	// Next raw timestamp wraps to a small value; expanded time must stay
	// strictly above prev.
	got := ExpandTimestamp(prev, 0x00000010)
	want := uint64(0x100000010)
	if got != want {
		t.Fatalf("wrap expansion = %#x, want %#x", got, want)
	}
	if got <= prev {
		t.Fatalf("expanded %d not monotonic over prev %d", got, prev)
	}
}

func TestExpandTimestampBackwardsJump(t *testing.T) {
	prev := uint64(100_000_000)
	// A large backwards excursion stays backwards instead of being hidden
	// by wrap arithmetic; the normalizer must surface it as a jump.
	got := ExpandTimestamp(prev, 10_000_000)
	if got >= prev {
		t.Fatalf("backwards jump expanded to %d >= prev %d", got, prev)
	}
}

func TestNormalizerRTPMonotonicAcrossWrap(t *testing.T) {
	tid := TrackID{Source: "src", Index: 0, Kind: KindVideo}
	// Threshold above the 2^32 wrap span so a genuine wrap is not mistaken
	// for a source-switch jump.
	n := NewTrackNormalizer(tid, ClockRef{Source: "src", Clock: 11}, 90000, 1<<33)
	raws := []uint32{0xFFFFFFF0, 0x00000010, 0x00000050}
	var prev Rational
	for i, raw := range raws {
		ts, err := n.PushRTP(pkt(raw, uint16(i), true))
		if err != nil || ts == nil {
			t.Fatalf("packet %d: ts=%v err=%v", i, ts, err)
		}
		if i > 0 {
			if ts.DTS.Cmp(prev) <= 0 {
				t.Fatalf("DTS %v not strictly after %v", ts.DTS, prev)
			}
			if ts.WrapCount != 1 {
				t.Fatalf("wrap count = %d, want 1", ts.WrapCount)
			}
		}
		prev = ts.DTS
	}
}

func TestNormalizerDuplicateTimestamp(t *testing.T) {
	tid := TrackID{Source: "src", Index: 0, Kind: KindAudio}
	n := NewTrackNormalizer(tid, ClockRef{Source: "src", Clock: 12}, 48000, 0)
	if _, err := n.PushRTP(pkt(1000, 1, false)); err != nil {
		t.Fatal(err)
	}
	ts, err := n.PushRTP(pkt(1000, 2, false))
	if err != nil {
		t.Fatal(err)
	}
	if ts != nil {
		t.Fatalf("duplicate timestamp must yield nil ts, got %+v", ts)
	}
	if len(n.Errors()) != 1 || n.Errors()[0].Kind != KindDuplicateTime || !n.Errors()[0].Recoverable {
		t.Fatalf("want one recoverable duplicate-timestamp error, got %+v", n.Errors())
	}
}

func TestNormalizerBoundaryJumpAndCorrection(t *testing.T) {
	tid := TrackID{Source: "src", Index: 0, Kind: KindVideo}
	n := NewTrackNormalizer(tid, ClockRef{Source: "src", Clock: 13}, 90000, 1000)
	ts, err := n.PushRTP(pkt(9000, 1, true))
	if err != nil {
		t.Fatal(err)
	}
	// Reset clock close to zero after a source switch.
	_, err = n.PushRTP(pkt(100, 2, true))
	if err == nil {
		t.Fatal("expected boundary-jump error")
	}
	se, ok := err.(*StageError)
	if !ok || se.Kind != KindBoundaryJump || !se.Recoverable {
		t.Fatalf("want recoverable boundary-jump StageError, got %#v", err)
	}
	// Repair by shifting the new clock just past the old timeline.
	offset := int64(9000) - int64(100) + 1
	fixed := n.ApplyCorrection(100, offset, se.Position)
	if fixed.DTS.Cmp(ts.DTS) <= 0 {
		t.Fatalf("corrected DTS %v must follow %v", fixed.DTS, ts.DTS)
	}
	if fixed.RawDTS != 9001 {
		t.Fatalf("corrected raw DTS = %d, want 9001", fixed.RawDTS)
	}
}

func TestNormalizerFailedTrackIsolation(t *testing.T) {
	tid := TrackID{Source: "src", Index: 1, Kind: KindAudio}
	n := NewTrackNormalizer(tid, ClockRef{Source: "src", Clock: 14}, 48000, 0)
	n.Fail("unrepairable jump")
	if !n.Failed() {
		t.Fatal("expected failed track")
	}
	if _, err := n.PushRTP(pkt(1, 1, false)); err == nil {
		t.Fatal("failed track must keep returning an error, not drop silently")
	}
	// A sibling normalizer is unaffected.
	other := NewTrackNormalizer(TrackID{Source: "src", Index: 2, Kind: KindVideo}, ClockRef{Source: "src", Clock: 15}, 90000, 0)
	if _, err := other.PushRTP(pkt(1, 1, false)); err != nil {
		t.Fatalf("sibling track must stay clean: %v", err)
	}
}

func TestRationalExact(t *testing.T) {
	r := FromTicks(1001, 30000) // VFR-style non-decimal tick
	wantNs := int64(33_366_666)
	if got := r.Nanoseconds(); got != wantNs {
		t.Fatalf("Nanoseconds = %d, want %d", got, wantNs)
	}
	if r.Cmp(FromTicks(1001, 30000)) != 0 {
		t.Fatal("equal rationals must compare equal")
	}
	diff := FromTicks(2, 10).Sub(FromTicks(1, 10))
	if diff.Num != 1 || diff.Den != 10 {
		t.Fatalf("subtraction = %v, want 1/10", diff)
	}
}
