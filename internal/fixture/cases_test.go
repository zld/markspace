package fixture

import (
	"testing"

	"medcheck/internal/model"
)

func TestCRCCorruptHasTwoBadFrames(t *testing.T) {
	set := CRCCorrupt()
	bad := 0
	for _, f := range set.Frames {
		if !f.Valid {
			bad++
			if f.Err == nil || f.Err.Kind != model.KindChecksum {
				t.Fatalf("bad frame missing checksum error: %+v", f)
			}
		}
	}
	if bad != 2 {
		t.Fatalf("bad frames = %d want 2", bad)
	}
}

func TestNormalAllValidWithOffsets(t *testing.T) {
	set := Normal()
	if len(set.Frames) != 8 {
		t.Fatalf("frames = %d", len(set.Frames))
	}
	for _, f := range set.Frames {
		if !f.Valid || f.Offset.End <= f.Offset.Start {
			t.Fatalf("frame not traceable/valid: %+v", f)
		}
	}
}

func TestSourceSwitchTwoSources(t *testing.T) {
	set := SourceSwitch()
	sources := map[uint32]bool{}
	for _, f := range set.Frames {
		sources[f.Source] = true
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %d want 2", len(sources))
	}
}

func TestLateFramesWithinWindow(t *testing.T) {
	base, late := LateFrames()
	if len(late) != 2 || base == nil {
		t.Fatalf("late frames = %d", len(late))
	}
	for _, f := range late {
		if f.EventTime.Ticks > 50 {
			t.Fatalf("late frame event %d must not exceed base max 50", f.EventTime.Ticks)
		}
	}
}

func TestMP4Samples(t *testing.T) {
	good, err := ValidMP4()
	if err != nil {
		t.Fatal(err)
	}
	if len(good.Data) == 0 || good.SampleCount != 9 {
		t.Fatal("valid mp4 not generated")
	}
	tr, err := TruncatedMP4()
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Data) <= len(good.Data) {
		t.Fatal("truncated sample must append bytes")
	}
}

func TestRTPAndWrap(t *testing.T) {
	d := DisorderedRTP()
	if len(d.Packets()) != len(d.Arrival) {
		t.Fatal("packet count mismatch")
	}
	w := WrapAround()
	if len(w.Raw) != len(w.Expected) {
		t.Fatal("wrap series length mismatch")
	}
}
