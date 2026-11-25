package verify

import (
	"testing"
	"time"
)

func TestBitrateDev(t *testing.T) {
	if d := BitrateDev(1_980_000, 1_800_000); d < 0.099 || d > 0.101 {
		t.Fatalf("dev = %v, want ~0.10", d)
	}
	if d := BitrateDev(1_800_000, 1_800_000); d != 0 {
		t.Fatalf("dev = %v, want 0", d)
	}
	if d := BitrateDev(100, 0); d != 0 {
		t.Fatalf("zero target: dev = %v, want 0", d)
	}
}

func TestParsePSNRAverage(t *testing.T) {
	log := "frame=  240 fps=0.0 q=-0.0 Lsize=N/A time=00:00:08.00\n" +
		"[Parsed_psnr_0 @ 0x13] PSNR y:41.234567 u:44.1 v:43.9 average:41.654321 min:38.1 max:48.2\n"
	v, err := ParsePSNRAverage(log)
	if err != nil {
		t.Fatal(err)
	}
	if v < 41.65 || v > 41.66 {
		t.Fatalf("psnr = %v", v)
	}
	if _, err := ParsePSNRAverage("no psnr here"); err == nil {
		t.Fatal("expected error for missing average")
	}
	v, err = ParsePSNRAverage("average:inf min:inf")
	if err != nil || v < 90 {
		t.Fatalf("inf handling: %v %v", v, err)
	}
}

func TestOutcomeAdd(t *testing.T) {
	o := Outcome{OK: true}
	o.add("a", true, "ok")
	if !o.OK {
		t.Fatal("must stay OK")
	}
	o.add("b", false, "bad")
	if o.OK {
		t.Fatal("must flip to not OK")
	}
	if len(o.Checks) != 2 {
		t.Fatalf("checks = %d", len(o.Checks))
	}
}

func TestDefaultConstraints(t *testing.T) {
	c := DefaultConstraints()
	if c.MaxBitrateDev != 0.10 || c.MinPSNR != 30.0 || c.MaxBoundaryErr != 100*time.Millisecond {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}
