package pipeline

import (
	"errors"
	"path/filepath"
	"testing"

	mpierr "mediapipeline/internal/errors"
	"mediapipeline/internal/fixture"
	mediartp "mediapipeline/internal/rtp"
)

// TestCorruptFixtureAbortsParse ensures a corrupted input stops the DAG at
// the parse node and preserves the structured error and its byte offset.
func TestCorruptFixtureAbortsParse(t *testing.T) {
	spec := fixture.Spec{
		Seed: 7, Packets: 16, SampleRate: fixture.OpusRate, StartSeq: 100,
		Corrupt: map[int]int{4: mediartp.FrameHeaderSize + 1},
	}
	out := filepath.Join(t.TempDir(), "c.mp4")
	cfg := baseCfg(t, "run-corrupt", out)
	cfg.AudioSpec = &spec
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rep := e.Run()
	if rep.FirstError == nil {
		t.Fatal("corruption must abort the pipeline")
	}
	if rep.FirstError.Category != mpierr.CatCorrupt {
		t.Fatalf("category = %s, want corrupt", rep.FirstError.Category)
	}
	want := e.GenMeta().CorruptOffsets[4]
	if rep.FirstError.ByteOffset != want {
		t.Fatalf("offset = %d, want %d", rep.FirstError.ByteOffset, want)
	}
	if rep.BlockedDependency == nil {
		t.Fatal("downstream nodes must be reported blocked")
	}
	if rep.Summary != "" {
		t.Fatal("no output digest allowed when parsing failed")
	}
}

// TestGapAndLateFixtureRecovers exercises out-of-window late packets and a
// dropped packet: the run still completes with recorded gaps and a valid,
// deterministic output.
func TestGapAndLateFixtureRecovers(t *testing.T) {
	spec := fixture.Spec{
		Seed: 7, Packets: 24, SampleRate: fixture.OpusRate, StartSeq: 100,
		Late: map[int]int{1: 18},
		Drop: map[int]bool{7: true},
	}
	out := filepath.Join(t.TempDir(), "g.mp4")
	cfg := baseCfg(t, "run-gaps", out)
	cfg.JitterWindow = 4
	cfg.AudioSpec = &spec
	e, _ := NewEngine(cfg)
	rep := e.Run()
	if rep.FirstError != nil {
		t.Fatalf("recoverable fixture must succeed: %v", rep.FirstError)
	}
	if rep.Gaps == 0 {
		t.Fatal("dropped packet must leave a recorded gap")
	}
	if rep.Packets == 0 || rep.Summary == "" {
		t.Fatalf("missing output: %+v", rep)
	}

	// A second engine over the same fault fixture reproduces the same digest.
	cfg2 := baseCfg(t, "run-gaps", filepath.Join(t.TempDir(), "g2.mp4"))
	cfg2.JitterWindow = 4
	cfg2.AudioSpec = &spec
	e2, _ := NewEngine(cfg2)
	rep2 := e2.Run()
	if rep2.FirstError != nil {
		t.Fatal(rep2.FirstError)
	}
	if rep2.Summary != rep.Summary {
		t.Fatalf("fault-fixture digest not deterministic: %s vs %s", rep2.Summary, rep.Summary)
	}
}

// TestRolloverFixtureProcessesAcrossBoundary drives a stream that crosses the
// 16-bit sequence boundary and confirms normal completion.
func TestRolloverFixtureProcessesAcrossBoundary(t *testing.T) {
	spec := fixture.Spec{
		Seed: 7, Packets: 8, SampleRate: fixture.OpusRate, StartSeq: 65532,
		Duplicate: map[int]bool{2: true},
	}
	out := filepath.Join(t.TempDir(), "r.mp4")
	cfg := baseCfg(t, "run-roll", out)
	cfg.AudioSpec = &spec
	e, _ := NewEngine(cfg)
	rep := e.Run()
	if rep.FirstError != nil {
		t.Fatalf("rollover fixture failed: %v", rep.FirstError)
	}
	if rep.Duplicates == 0 {
		t.Fatal("injected duplicate must be counted")
	}
}

// TestStrictRecoveryRejectsGap makes gaps fatal under the strict policy.
func TestStrictRecoveryRejectsGap(t *testing.T) {
	spec := fixture.Spec{
		Seed: 7, Packets: 12, SampleRate: fixture.OpusRate, StartSeq: 100,
		Drop: map[int]bool{3: true},
	}
	out := filepath.Join(t.TempDir(), "s.mp4")
	cfg := baseCfg(t, "run-strict", out)
	cfg.AudioSpec = &spec
	cfg.StrictRecovery = true
	e, _ := NewEngine(cfg)
	rep := e.Run()
	if rep.FirstError == nil {
		t.Fatal("strict policy must reject a gap")
	}
	if rep.FirstError.Category != mpierr.CatGap {
		t.Fatalf("category = %s, want gap", rep.FirstError.Category)
	}
}

// TestTruncatedFixtureIsClassified confirms a mid-packet cut yields a
// CatTruncated structured error rather than a bare EOF.
func TestTruncatedFixtureIsClassified(t *testing.T) {
	full, _, err := fixture.GenerateAudio(
		fixture.Spec{Seed: 7, Packets: 12, SampleRate: fixture.OpusRate, StartSeq: 100})
	if err != nil {
		t.Fatal(err)
	}
	spec := fixture.Spec{Seed: 7, Packets: 12, SampleRate: fixture.OpusRate,
		StartSeq: 100, TruncateAt: len(full) - 5}
	out := filepath.Join(t.TempDir(), "t.mp4")
	cfg := baseCfg(t, "run-trunc", out)
	cfg.AudioSpec = &spec
	e, _ := NewEngine(cfg)
	rep := e.Run()
	var me *mpierr.MediaError
	if !errors.As(rep.FirstError, &me) || me.Category != mpierr.CatTruncated {
		t.Fatalf("want CatTruncated, got %v", rep.FirstError)
	}
}
