package verify_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"continuity/fixture"
	"continuity/manifest"
	"continuity/media"
	"continuity/planner"
	"continuity/segment"
	"continuity/verify"
	"continuity/worker"
)

func buildOutput(t *testing.T) (string, *manifest.Manifest, []*media.Container, *planner.Plan) {
	t.Helper()
	dir := t.TempDir()
	var inputs []*media.Container
	for _, name := range []string{"a", "b"} {
		p := filepath.Join(dir, name+".mp4")
		if err := fixture.Generate(p, fixture.ValidSpec()); err != nil {
			t.Fatal(err)
		}
		c, err := media.Probe(p)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, c)
	}
	plan, _, err := planner.Evaluate(inputs, 2_000_000_000, planner.Constraints{
		MaxBufferBytes: 1 << 20, MaxConcurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	res, err := segment.Cut(context.Background(), inputs, plan, out, &worker.Pool{Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	m := manifest.Build(inputs, plan, res)
	if err := manifest.Write(out, m); err != nil {
		t.Fatal(err)
	}
	return out, m, inputs, plan
}

func TestVerifyValidOutput(t *testing.T) {
	out, m, inputs, plan := buildOutput(t)
	rep, err := verify.Check(out, m, inputs, plan, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Verified || rep.RoundtripRate != 1.0 || rep.AlignedBoundaryDeltaNs != 0 {
		t.Fatalf("report %+v", rep)
	}
}

func TestVerifyDetectsCorruptedSegment(t *testing.T) {
	out, m, inputs, plan := buildOutput(t)
	victim := filepath.Join(out, m.Segments[0].File)
	if err := os.WriteFile(victim, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := verify.Check(out, m, inputs, plan, 1.0)
	if !media.IsKind(err, media.KindVerify) {
		t.Fatalf("want verify error, got %v", err)
	}
	if rep.Verified {
		t.Fatal("corrupted output must not verify")
	}
	if rep.RoundtripRate >= 1.0 {
		t.Fatalf("roundtrip rate %.3f should be below 1", rep.RoundtripRate)
	}
}

func TestVerifyDetectsBoundaryMisalignment(t *testing.T) {
	out, m, inputs, plan := buildOutput(t)
	// Shift one representation's boundary far beyond one unified tick.
	for i := range m.Segments {
		if m.Segments[i].Rep == 1 && m.Segments[i].Index == 1 {
			m.Segments[i].StartNanos += 5_000_000
		}
	}
	rep, err := verify.Check(out, m, inputs, plan, 1.0)
	if !media.IsKind(err, media.KindVerify) {
		t.Fatalf("want verify error, got %v", err)
	}
	if rep.AlignedBoundaryDeltaNs < 5_000_000 {
		t.Fatalf("delta %d not reported", rep.AlignedBoundaryDeltaNs)
	}
}
