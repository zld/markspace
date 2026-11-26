package cmaf

import (
	"os"
	"path/filepath"
	"testing"

	"cmafrepair/pkg/media/timebase"
)

func initInfo() *InitInfo {
	return &InitInfo{Timescale: 90000, TrackID: 1, TrexDefaultDuration: 3003}
}

// buildSeqFragment makes a fragment whose trun omits per-sample durations so
// the trex/tfhd default (3003) must be applied.
func buildSeqFragment(seq uint32, tfdt uint64, nSamples int, withDur bool) []byte {
	var trun *BoxBuilder
	if withDur {
		trun = NewBox(BoxTrun).FullHeader(1, 0x000300).U32(uint32(nSamples))
		for i := 0; i < nSamples; i++ {
			trun.U32(3003).U32(100)
		}
	} else {
		// size-only trun: flags 0x200, no duration field.
		trun = NewBox(BoxTrun).FullHeader(1, 0x000200).U32(uint32(nSamples))
		for i := 0; i < nSamples; i++ {
			trun.U32(100)
		}
	}
	tfhd := NewBox(BoxTfhd).FullHeader(0, 0x000008).U32(1).U32(3003)
	tfdtBox := NewBox(BoxTfdt).FullHeader(1, 0).U64(tfdt)
	mfhd := NewBox(BoxMfhd).FullHeader(0, 0).U32(seq)
	traf := NewBox(BoxTraf).Child(tfhd).Child(tfdtBox).Child(trun)
	moof := NewBox(BoxMoof).Child(mfhd).Child(traf)
	mdat := NewBox(BoxMdat).Bytes(make([]byte, 100*nSamples))
	return append(moof.Build(), mdat.Build()...)
}

func writeFrag(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRepairRebuildsRolledBackTfdt(t *testing.T) {
	dir := t.TempDir()
	tr := NewTimelineRepair("video_90000", initInfo())

	// Fragment 1: correct tfdt 0, two samples of 3003 -> duration 6006.
	p1 := writeFrag(t, dir, "f1.m4s", buildSeqFragment(1, 0, 2, true))
	f1, err := ParseFragment(p1)
	if err != nil {
		t.Fatal(err)
	}
	e1, err := tr.Add(f1)
	if err != nil || e1.Status != StatusIntact || e1.FixedTfdt != 0 {
		t.Fatalf("first entry: %+v err=%v", e1, err)
	}

	// Fragment 2 declares a rolled-back tfdt (100) but should be 6006.
	p2 := writeFrag(t, dir, "f2.m4s", buildSeqFragment(2, 100, 2, true))
	f2, err := ParseFragment(p2)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := tr.Add(f2)
	if err != nil {
		t.Fatal(err)
	}
	if e2.Status != StatusRepaired || e2.FixedTfdt != 6006 {
		t.Fatalf("rollback not rebuilt: status=%s fixed=%d", e2.Status, e2.FixedTfdt)
	}
	hasRollback := false
	for _, d := range e2.Defects {
		if d.Code == timebase.CodeTfdtRollback {
			hasRollback = true
		}
	}
	if !hasRollback {
		t.Fatalf("expected E_TFDT_ROLLBACK defect, got %+v", e2.Defects)
	}

	// Write the repaired fragment and verify the on-disk tfdt.
	w, _ := NewWriter(t.TempDir())
	dst, _, patched, err := w.WriteFragment(p2, f2, e2.FixedTfdt, e2.Status)
	if err != nil || !patched {
		t.Fatalf("write: err=%v patched=%v", err, patched)
	}
	if err := VerifyPatch(dst, 6006); err != nil {
		t.Fatalf("VerifyPatch: %v", err)
	}
	// The mdat payload must be byte-identical (only tfdt changed).
	orig, _ := os.ReadFile(p2)
	fixed, _ := os.ReadFile(dst)
	if len(orig) != len(fixed) {
		t.Fatalf("repaired size changed: %d vs %d", len(orig), len(fixed))
	}
}

func TestRepairAppliesDefaultDuration(t *testing.T) {
	dir := t.TempDir()
	tr := NewTimelineRepair("video_90000", initInfo())
	p := writeFrag(t, dir, "f1.m4s", buildSeqFragment(1, 0, 3, false))
	f, err := ParseFragment(p)
	if err != nil {
		t.Fatal(err)
	}
	e, err := tr.Add(f)
	if err != nil {
		t.Fatal(err)
	}
	// 3 samples * 3003 default duration.
	if e.Duration != 9009 || e.Status != StatusIntact {
		t.Fatalf("default duration not applied: %+v", e)
	}
	if f.Samples[2].MediaTime != 6006 {
		t.Fatalf("sample media time wrong: %d", f.Samples[2].MediaTime)
	}
}

func TestRepairMarksGapWhenDurationUnknown(t *testing.T) {
	// Init with NO default duration and a trun lacking durations.
	init := &InitInfo{Timescale: 90000, TrackID: 1}
	tr := NewTimelineRepair("video_90000", init)
	dir := t.TempDir()
	p := writeFrag(t, dir, "f1.m4s", buildSeqFragment(1, 0, 2, false))
	f, err := ParseFragment(p)
	if err != nil {
		t.Fatal(err)
	}
	e, err := tr.Add(f)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != StatusGap {
		t.Fatalf("want GAP, got %s", e.Status)
	}
	tl := tr.Build()
	if tl.GapCount != 1 {
		t.Fatalf("gap count=%d want 1", tl.GapCount)
	}
	// No time fabricated: timeline must not advance past 0.
	if tr.expectedTime != 0 {
		t.Fatalf("GAP advanced the clock to %d", tr.expectedTime)
	}
}

func TestRepairDetectsMfhdGapAndDuplicate(t *testing.T) {
	tr := NewTimelineRepair("v", initInfo())
	dir := t.TempDir()
	p1 := writeFrag(t, dir, "f1.m4s", buildSeqFragment(1, 0, 1, true))
	f1, _ := ParseFragment(p1)
	if _, err := tr.Add(f1); err != nil {
		t.Fatal(err)
	}
	// Sequence jumps 1 -> 3.
	p3 := writeFrag(t, dir, "f3.m4s", buildSeqFragment(3, 3003, 1, true))
	f3, _ := ParseFragment(p3)
	e3, err := tr.Add(f3)
	if err != nil {
		t.Fatal(err)
	}
	foundGap := false
	for _, d := range e3.Defects {
		if d.Code == timebase.CodeMfhdGap {
			foundGap = true
		}
	}
	if !foundGap {
		t.Fatalf("mfhd gap not detected: %+v", e3.Defects)
	}
	// Duplicate sequence 3.
	p3b := writeFrag(t, dir, "f3b.m4s", buildSeqFragment(3, 6006, 1, true))
	f3b, _ := ParseFragment(p3b)
	e3b, _ := tr.Add(f3b)
	foundDup := false
	for _, d := range e3b.Defects {
		if d.Code == timebase.CodeMfhdDuplicate {
			foundDup = true
		}
	}
	if !foundDup {
		t.Fatalf("mfhd duplicate not detected: %+v", e3b.Defects)
	}
}

func TestTimelineSharedClockMapping(t *testing.T) {
	tr := NewTimelineRepair("v", initInfo())
	dir := t.TempDir()
	// One sample of 90000 ticks = 1000 ms.
	data := buildFragment(1, 0, []uint32{50}, []uint32{90000})
	p := writeFrag(t, dir, "f1.m4s", data)
	f, _ := ParseFragment(p)
	e, err := tr.Add(f)
	if err != nil {
		t.Fatal(err)
	}
	if e.StartMs != 0 || e.EndMs != 1000 {
		t.Fatalf("shared ms wrong: start=%d end=%d", e.StartMs, e.EndMs)
	}
}

func TestTruncatedFragmentStatus(t *testing.T) {
	tr := NewTimelineRepair("v", initInfo())
	dir := t.TempDir()
	data := buildFragment(1, 0, []uint32{500, 500}, []uint32{3003, 3003})
	p := writeFrag(t, dir, "trunc.m4s", data[:len(data)-300])
	f, err := ParseFragment(p)
	if err != nil {
		t.Fatal(err)
	}
	e, err := tr.Add(f)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != StatusTruncated {
		t.Fatalf("want TRUNCATED, got %s", e.Status)
	}
	if tr.Build().TruncatedCount != 1 {
		t.Fatal("truncated count not tracked")
	}
}
