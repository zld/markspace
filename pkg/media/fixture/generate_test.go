package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"cmafrepair/pkg/media/clock"
	"cmafrepair/pkg/media/cmaf"
	"cmafrepair/pkg/media/timebase"
)

func filesEqual(a, b string) bool {
	ba, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	bb, err := os.ReadFile(b)
	if err != nil || len(ba) != len(bb) {
		return false
	}
	for i := range ba {
		if ba[i] != bb[i] {
			return false
		}
	}
	return true
}

func TestGenerateCreatesDatasets(t *testing.T) {
	dir := t.TempDir()
	sum, err := Generate(dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Representations != 3 || sum.FragmentsPerRep != 24 || len(sum.Datasets) != 3 {
		t.Fatalf("summary wrong: %+v", sum)
	}
	for _, ds := range Datasets {
		for _, r := range Reps {
			initP := filepath.Join(dir, ds, r.Name, "init.mp4")
			info, err := cmaf.ParseInit(initP)
			if err != nil {
				t.Fatalf("init %s/%s: %v", ds, r.Name, err)
			}
			if info.Timescale != r.Timescale {
				t.Fatalf("timescale %s: got %d want %d", r.Name, info.Timescale, r.Timescale)
			}
			for i := 1; i <= FragmentsPerRep; i++ {
				p := filepath.Join(dir, ds, r.Name, fragFile(i))
				if _, err := cmaf.ParseFragment(p); err != nil {
					t.Fatalf("fragment %s/%s/%d: %v", ds, r.Name, i, err)
				}
			}
		}
	}
}

func fragFile(i int) string {
	return "frag_" + pad4(uint32(i)) + ".m4s"
}

func pad4(v uint32) string {
	const digits = "0000"
	b := []byte(digits)
	s := []byte{
		'0' + byte(v/1000),
		'0' + byte((v/100)%10),
		'0' + byte((v/10)%10),
		'0' + byte(v%10),
	}
	copy(b, s)
	return string(b)
}

func TestFaultsAreIndependentlyLocatable(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, 7); err != nil {
		t.Fatal(err)
	}
	rep := Reps[0]
	base := filepath.Join(dir, DSFaults, rep.Name)
	var mfhdGap, tfdtRollback, mdatTrunc int

	tr := cmaf.NewTimelineRepair(rep.Name, mustInit(t, base))
	for i := 1; i <= FragmentsPerRep; i++ {
		f, err := cmaf.ParseFragment(filepath.Join(base, fragFile(i)))
		if err != nil {
			t.Fatalf("frag %d hard error: %v", i, err)
		}
		for _, d := range f.Defects {
			switch d.Code {
			case timebase.CodeMdatTruncated:
				mdatTrunc++
			}
		}
		entry, err := tr.Add(f)
		if err != nil {
			t.Fatalf("repair frag %d: %v", i, err)
		}
		for _, d := range entry.Defects {
			switch d.Code {
			case timebase.CodeMfhdGap:
				mfhdGap++
			case timebase.CodeTfdtRollback:
				tfdtRollback++
			}
		}
	}
	if mfhdGap != 1 || tfdtRollback != 1 || mdatTrunc != 1 {
		t.Fatalf("defect counts: mfhdGap=%d tfdtRollback=%d mdatTrunc=%d, want 1/1/1",
			mfhdGap, tfdtRollback, mdatTrunc)
	}
}

func mustInit(t *testing.T, dir string) *cmaf.InitInfo {
	t.Helper()
	info, err := cmaf.ParseInit(filepath.Join(dir, "init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestEdgeDatasetWrapsRtpOnce(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, 7); err != nil {
		t.Fatal(err)
	}
	af, err := LoadAnchors(filepath.Join(dir, DSEdge, "anchors.json"))
	if err != nil {
		t.Fatal(err)
	}
	rec := af.Reps[Reps[0].Name]
	u := clock.NewUnwrapper(rec.ClockHz)
	var wraps int
	for _, ts := range rec.Timestamps {
		_, ev, err := u.Observe(ts)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Wrapped {
			wraps++
		}
	}
	if wraps != 1 || u.Wraps() != 1 {
		t.Fatalf("edge wraps=%d (counter=%d), want exactly 1", wraps, u.Wraps())
	}
	if u.Rollbacks() != 0 {
		t.Fatalf("edge must not report rollbacks, got %d", u.Rollbacks())
	}
}

func TestCleanDatasetHasNoWrapsOrRollbacks(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, 7); err != nil {
		t.Fatal(err)
	}
	af, err := LoadAnchors(filepath.Join(dir, DSClean, "anchors.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, rec := range af.Reps {
		u := clock.NewUnwrapper(rec.ClockHz)
		for _, ts := range rec.Timestamps {
			if _, _, err := u.Observe(ts); err != nil {
				t.Fatal(err)
			}
		}
		if u.Wraps() != 0 || u.Rollbacks() != 0 {
			t.Fatalf("%s clean stream had wraps=%d rollbacks=%d", name, u.Wraps(), u.Rollbacks())
		}
	}
}

func TestDeterministicSeeds(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	if _, err := Generate(d1, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(d2, 7); err != nil {
		t.Fatal(err)
	}
	p1 := filepath.Join(d1, DSClean, Reps[0].Name, fragFile(1))
	p2 := filepath.Join(d2, DSClean, Reps[0].Name, fragFile(1))
	// Same seed must produce identical content (hashes compared by the
	// checkpoint/cache layer; here compare raw bytes).
	if !filesEqual(p1, p2) {
		t.Fatal("identical seed must produce byte-identical fixtures")
	}
}
