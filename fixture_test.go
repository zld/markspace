package mediakernel

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestFixtureDeterministic(t *testing.T) {
	f1, err := GenerateFixture(FixtureSpec{Seed: 7, PacketCount: 24})
	if err != nil {
		t.Fatal(err)
	}
	f2, err := GenerateFixture(FixtureSpec{Seed: 7, PacketCount: 24})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f1.Data, f2.Data) {
		t.Fatal("identical specs must produce identical bytes")
	}
	f3, _ := GenerateFixture(FixtureSpec{Seed: 8, PacketCount: 24})
	if bytes.Equal(f1.Data, f3.Data) {
		t.Fatal("different seeds must produce different streams")
	}
}

func TestFixtureCleanStreamLossesDuplicatesWraps(t *testing.T) {
	fx, err := GenerateFixture(FixtureSpec{Seed: 7, PacketCount: 24})
	if err != nil {
		t.Fatal(err)
	}
	if fx.DesignedLosses == 0 || fx.DesignedDuplicates == 0 || fx.DesignedWraps == 0 {
		t.Fatalf("fixture must contain losses, duplicates and a wrap: %+v", fx)
	}
	reader := NewFrameReader(bytes.NewReader(fx.Data))
	tracker := NewGapTracker()
	timeline := NewTimeline(NewClock(DefaultClockRate))
	parsed := 0
	for {
		pkt, err := reader.ReadPacket()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("clean fixture must parse: %v", err)
		}
		tracker.SetCodec(pkt.Codec)
		dup, missing, gerr := tracker.Observe(pkt.Seq)
		if gerr != nil {
			t.Fatalf("clean fixture sequence error: %v", gerr)
		}
		if dup {
			continue // duplicates never advance the clock
		}
		for range missing {
			timeline.Reserve(tracker.GapSamples())
		}
		timeline.Advance(pkt.ValidSamples)
		parsed++
	}
	if tracker.GapCount() != fx.DesignedLosses {
		t.Fatalf("gap count %d != designed %d", tracker.GapCount(), fx.DesignedLosses)
	}
	if tracker.DuplicateCount() != fx.DesignedDuplicates {
		t.Fatalf("dup count %d != designed %d", tracker.DuplicateCount(), fx.DesignedDuplicates)
	}
	if tracker.WrapCount() != fx.DesignedWraps {
		t.Fatalf("wrap count %d != designed %d", tracker.WrapCount(), fx.DesignedWraps)
	}
	// Every written unique frame is parsed once; duplicates arrive on the wire
	// but are excluded by the tracker, and losses never produce a frame.
	if parsed != fx.WrittenFrames {
		t.Fatalf("unique parsed=%d should equal written unique frames %d",
			parsed, fx.WrittenFrames)
	}
	// The timeline spans unique packet samples plus reserved gap ranges.
	if timeline.Cursor() <= 0 {
		t.Fatal("timeline must advance across the stream")
	}
}

func TestFixtureCorruptModes(t *testing.T) {
	// Invalid duration fails at parse with the structured error + offset.
	fx, err := GenerateFixture(FixtureSpec{Seed: 7, Corrupt: CorruptInvalidDuration})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainForTest(fx.Data); err == nil {
		t.Fatal("invalid-duration fixture must fail parsing")
	} else if me, ok := AsMediaError(err); !ok || me.Phase != PhaseParse || me.Offset < 0 {
		t.Fatalf("want offset-bearing parse error, got %v", err)
	}

	// Bad sequence fails at the sequence phase.
	fx2, err := GenerateFixture(FixtureSpec{Seed: 7, Corrupt: CorruptSequence})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainForTest(fx2.Data); err == nil {
		t.Fatal("bad-sequence fixture must fail")
	} else if me, ok := AsMediaError(err); !ok || me.Phase != PhaseSequence {
		t.Fatalf("want sequence-phase error, got %v", err)
	}

	// Truncated byte stream fails with a truncated-body parse error at offset.
	fx3, err := GenerateFixture(FixtureSpec{Seed: 7, Corrupt: CorruptTruncated})
	if err != nil {
		t.Fatal(err)
	}
	cut := TruncateFrame(fx3.Data)
	if _, err := drainForTest(cut); err == nil {
		t.Fatal("truncated fixture must fail")
	} else if me, ok := AsMediaError(err); !ok || me.Phase != PhaseParse || me.Offset < 0 {
		t.Fatalf("want offset-bearing truncation error, got %v", err)
	}
}

func drainForTest(data []byte) (int, error) {
	reader := NewFrameReader(bytes.NewReader(data))
	tracker := NewGapTracker()
	n := 0
	for {
		pkt, err := reader.ReadPacket()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		dup, _, gerr := tracker.Observe(pkt.Seq)
		if gerr != nil {
			return n, gerr
		}
		if !dup {
			n++
		}
	}
}
