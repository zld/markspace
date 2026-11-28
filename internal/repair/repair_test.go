package repair

import (
	"path/filepath"
	"testing"

	"avpipe/internal/fixture"
	"avpipe/internal/model"
	"avpipe/internal/scan"
)

func scanCase(t *testing.T, name string) *scan.Index {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := fixture.WriteCase(dir, fixture.Cases()[name]); err != nil {
		t.Fatalf("write case: %v", err)
	}
	idx, err := scan.Scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return idx
}

func TestRepairNormalBoundaries(t *testing.T) {
	rep := Repair(scanCase(t, "normal"))
	if len(rep.Boundaries) != 6 {
		t.Fatalf("boundaries=%d want 6", len(rep.Boundaries))
	}
	if rep.Repairable != 2 {
		t.Fatalf("repairable=%d want 2", rep.Repairable)
	}
	var wrap, sw int
	for _, c := range rep.Corrections {
		switch c.Kind {
		case model.KindWrap:
			wrap++
		case model.KindSwitch:
			sw++
		}
		if c.Offset < 0 || c.FixedTimeNS == c.RawTimeNS && c.Kind == model.KindWrap {
			t.Fatalf("correction lacks audit data: %+v", c)
		}
	}
	if wrap != 1 || sw != 1 {
		t.Fatalf("want 1 wrap + 1 switch, got wrap=%d switch=%d", wrap, sw)
	}
	for _, tr := range rep.Tracks {
		if tr.Stopped || tr.Defect != nil {
			t.Fatalf("track %d should be clean, got %+v", tr.Track, tr.Defect)
		}
		var prev model.TimeNS = -1
		for _, s := range tr.Samples {
			if s.FixedTimeNS <= prev {
				t.Fatalf("track %d not monotonic", tr.Track)
			}
			prev = s.FixedTimeNS
		}
	}
}

func TestWrapSpansModulusMonotonically(t *testing.T) {
	idx := scanCase(t, "normal")
	rep := Repair(idx)
	var tr *TrackResult
	for i := range rep.Tracks {
		if rep.Tracks[i].Track == 1 {
			tr = &rep.Tracks[i]
		}
	}
	if tr == nil {
		t.Fatal("track 1 missing")
	}
	// The corrected timeline must keep moving forward even across the wrap.
	if len(tr.Samples) != 8 {
		t.Fatalf("wrapped track samples=%d want 8", len(tr.Samples))
	}
	var c *model.Correction
	for i := range tr.Corrections {
		if tr.Corrections[i].Kind == model.KindWrap {
			c = &tr.Corrections[i]
		}
	}
	if c == nil || c.ShiftNS <= 0 || c.Offset != tr.Samples[4].InputOffset {
		t.Fatalf("bad wrap correction: %+v", c)
	}
}

func TestIllegalBoundaryStopsOnlyItsTrack(t *testing.T) {
	rep := Repair(scanCase(t, "damaged"))
	var stopped, others int
	for _, tr := range rep.Tracks {
		if tr.Track == 2 {
			if !tr.Stopped || tr.Defect == nil || tr.Defect.Recoverable {
				t.Fatalf("track 2 must stop with fatal defect: %+v", tr.Defect)
			}
			if tr.Defect.Code != model.CodeIllegalTimestamp || tr.Defect.Offset < 0 {
				t.Fatalf("unexpected defect: %+v", tr.Defect)
			}
			stopped++
		} else if !tr.Stopped {
			others++
		}
	}
	if stopped != 1 || others != 2 {
		t.Fatalf("want 1 stopped, 2 healthy; got stopped=%d others=%d", stopped, others)
	}
}

func TestTruncationRecoverableAndIsolated(t *testing.T) {
	rep := Repair(scanCase(t, "damaged"))
	for _, tr := range rep.Tracks {
		if tr.Track == 3 {
			if tr.Defect == nil || !tr.Defect.Recoverable ||
				tr.Defect.Code != model.CodeTruncated || tr.Defect.Offset < 0 {
				t.Fatalf("track 3 truncation not reported recoverably: %+v", tr.Defect)
			}
			if tr.Stopped {
				t.Fatal("truncation must not mark the track stopped")
			}
		}
	}
}

func TestIsWrapClassifier(t *testing.T) {
	if !model.IsWrap(0xFFFFFFF0, 0x00000010) {
		t.Fatal("small forward distance after zero should be a wrap")
	}
	if model.IsWrap(100, 200) {
		t.Fatal("plain forward step is not a wrap")
	}
	if model.IsWrap(0x80000000, 0) {
		t.Fatal("exactly half modulus is ambiguous, not a wrap")
	}
}
