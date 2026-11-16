package demo_test

import (
	"bytes"
	"testing"

	"hlscheck/internal/demo"
	"hlscheck/internal/ts"
)

func TestDeterministic(t *testing.T) {
	opt := demo.GenOptions{BitrateKbps: 900, DurationSec: 4, GOPSec: 2, Seed: 42}
	var a, b, c bytes.Buffer
	if err := demo.Generate(&a, opt); err != nil {
		t.Fatal(err)
	}
	if err := demo.Generate(&b, opt); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("same seed produced different streams")
	}
	opt.Seed = 43
	if err := demo.Generate(&c, opt); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.Bytes(), c.Bytes()) {
		t.Error("different seeds produced identical streams")
	}
}

// Profiles of different bitrates must share the same PTS timeline so
// that sliced outputs stay aligned.
func TestProfilesShareTimeline(t *testing.T) {
	var lo, hi bytes.Buffer
	if err := demo.Generate(&lo, demo.GenOptions{BitrateKbps: 450, DurationSec: 4, GOPSec: 2, Seed: 1}); err != nil {
		t.Fatal(err)
	}
	if err := demo.Generate(&hi, demo.GenOptions{BitrateKbps: 1800, DurationSec: 4, GOPSec: 2, Seed: 2}); err != nil {
		t.Fatal(err)
	}
	f1, l1, n1, _, err := ts.ScanPTS(bytes.NewReader(lo.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	f2, l2, n2, _, err := ts.ScanPTS(bytes.NewReader(hi.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if f1 != f2 || l1 != l2 || n1 != n2 {
		t.Errorf("timelines differ: (%d,%d,%d) vs (%d,%d,%d)", f1, l1, n1, f2, l2, n2)
	}
}

func TestParseFault(t *testing.T) {
	f, err := demo.ParseFault("drop", "900:4")
	if err != nil || f.Profile != 900 || f.A != 4 {
		t.Errorf("drop parse: %+v err=%v", f, err)
	}
	f, err = demo.ParseFault("swap", "1800:2,3")
	if err != nil || f.A != 2 || f.B != 3 {
		t.Errorf("swap parse: %+v err=%v", f, err)
	}
	for _, bad := range []string{"", "900", "x:1", "900:a", "900:1:2"} {
		if _, err := demo.ParseFault("drop", bad); err == nil {
			t.Errorf("drop %q: want error", bad)
		}
	}
	if _, err := demo.ParseFault("swap", "900:3,3"); err == nil {
		t.Error("swap with equal indices: want error")
	}
}
