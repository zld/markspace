package media

import (
	"testing"
)

func TestClockConversions(t *testing.T) {
	c := Clock{VideoTimescale: 1000, AudioClockRate: 48000}
	if got := c.VideoToNanos(100); got != 100_000_000 {
		t.Fatalf("video 100 ticks = %d ns, want 100000000", got)
	}
	if got := c.AudioToNanos(960); got != 20_000_000 {
		t.Fatalf("audio 960 ticks = %d ns, want 20000000", got)
	}
	// Conversion must stay exact for the fixture timeline.
	if got := NanosToMs(c.VideoToNanos(2500)); got != 2500.0 {
		t.Fatalf("2500 ticks -> %v ms, want 2500", got)
	}
}

func TestFrameHashVerification(t *testing.T) {
	cfg := DefaultConfig("normal", 17)
	fx := Generate(cfg)
	f := fx.Renditions[0].Frames[5]
	if !f.VerifyHash() {
		t.Fatalf("clean fixture frame failed hash check")
	}
	bad := f
	bad.Payload = append([]byte(nil), f.Payload...)
	bad.Payload[0] ^= 0x01
	if bad.VerifyHash() {
		t.Fatalf("mutated payload unexpectedly matched declared hash")
	}
}

func TestFixtureDeterministic(t *testing.T) {
	a := Generate(DefaultConfig("normal", 17))
	b := Generate(DefaultConfig("normal", 17))
	if len(a.Renditions) != len(b.Renditions) {
		t.Fatalf("rendition count drift: %d vs %d", len(a.Renditions), len(b.Renditions))
	}
	for i := range a.Renditions {
		ra, rb := a.Renditions[i], b.Renditions[i]
		if ra.TaskKey != rb.TaskKey {
			t.Fatalf("task key drift: %q vs %q", ra.TaskKey, rb.TaskKey)
		}
		for j := range ra.Frames {
			fa, fb := ra.Frames[j], rb.Frames[j]
			if fa.DeclaredSHA256 != fb.DeclaredSHA256 ||
				string(fa.Payload) != string(fb.Payload) {
				t.Fatalf("non-deterministic frame %s/%d", ra.Config.Name, j)
			}
		}
	}
	if len(a.Delivered) != len(b.Delivered) {
		t.Fatalf("delivery length drift")
	}
	for i := range a.Delivered {
		if a.Delivered[i].SequenceNumber != b.Delivered[i].SequenceNumber {
			t.Fatalf("delivery order drift at %d", i)
		}
	}
}

func TestCorruptionKinds(t *testing.T) {
	base := DefaultConfig("corrupt", 17)
	base.Corrupt = Corruption{
		TruncateAt: 3, BadChecksumAt: 7, DuplicateAt: 11,
		MissingAt: 15, OutOfRangeAt: 19,
	}
	fx := Generate(base)
	frames := fx.Renditions[0].Frames
	// Faults keep their original index unless an earlier MissingAt removed a frame.
	indexOf := func(orig int) int {
		if orig > base.Corrupt.MissingAt {
			return orig - 1
		}
		return orig
	}
	if frames[indexOf(3)].VerifyHash() {
		t.Fatal("truncated frame should fail hash")
	}
	if frames[indexOf(7)].VerifyHash() {
		t.Fatal("bad-checksum frame should fail hash")
	}
	dup := indexOf(base.Corrupt.DuplicateAt)
	if frames[dup].Seq != frames[dup-1].Seq {
		t.Fatal("duplicate sequence not injected")
	}
	if len(frames) != base.Renditions[0].FrameCount-1 {
		t.Fatalf("missing frame not removed: %d", len(frames))
	}
	for _, f := range frames {
		if f.Seq < 0 {
			return
		}
	}
	t.Fatal("out-of-range sequence not found")
}

func TestUnrecoverableFixtureHasConsecutiveLoss(t *testing.T) {
	cfg := DefaultConfig("normal", 17)
	fx := GenerateUnrecoverable(cfg)
	delivered := map[uint16]bool{}
	for _, p := range fx.Delivered {
		delivered[p.SequenceNumber] = true
	}
	for _, seq := range []uint16{120, 121, 122} {
		if delivered[seq] {
			t.Fatalf("packet %d should be missing", seq)
		}
		if !fx.AudioGround[seq].InjectedLost {
			t.Fatalf("packet %d not marked injected-lost", seq)
		}
	}
}
