package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"mediapipeline/internal/fault"
	"mediapipeline/internal/fixture"
	"mediapipeline/internal/track"
)

func writeTemp(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunRemuxesLosslessly(t *testing.T) {
	dir := t.TempDir()
	p := fixture.BuildValid(fixture.Options{EventDuplicates: true})
	in := writeTemp(t, dir, "in.mp4", p.Data)
	out := filepath.Join(dir, "out.mp4")
	sum, err := Run(dir, in, out, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Committed || sum.PayloadSHA256In != sum.PayloadSHA256Out {
		t.Fatalf("summary: %+v", sum)
	}
	if sum.Duplicates != 1 || sum.EventsOut != sum.EventsIn-1 {
		t.Fatalf("dedup: in=%d out=%d dups=%d", sum.EventsIn, sum.EventsOut, sum.Duplicates)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
	// Re-running produces the same bytes, so the digest is reproducible.
	sum2, err := Run(dir, in, out, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if sum2.PayloadSHA256Out != sum.PayloadSHA256Out || sum2.OutputBytes != sum.OutputBytes {
		t.Fatalf("not reproducible: %+v vs %+v", sum, sum2)
	}
}

func TestRunUnknownFormatFailsAtProbe(t *testing.T) {
	dir := t.TempDir()
	in := writeTemp(t, dir, "in.bin", []byte("this is not media at all"))
	sum, err := Run(dir, in, filepath.Join(dir, "out.mp4"), Options{})
	if err == nil {
		t.Fatal("expected error")
	}
	if sum.ErrorStage != string(fault.StageProbe) || sum.Committed {
		t.Fatalf("summary: %+v", sum)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "out.mp4")); statErr == nil {
		t.Fatal("no output may be committed on failure")
	}
}

func TestRunDamagedLeavesNoOutput(t *testing.T) {
	dir := t.TempDir()
	p := fixture.BuildTruncatedBox(fixture.Options{})
	in := writeTemp(t, dir, "truncated.mp4", p.Data)
	out := filepath.Join(dir, "out.mp4")
	sum, err := Run(dir, in, out, Options{})
	if err == nil || sum.Committed {
		t.Fatalf("expected failure: %+v", sum)
	}
	if sum.ErrorStage != "demux" || sum.InputOffset != int64(len(p.Data)) {
		t.Fatalf("summary: %+v", sum)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatal("damaged input must not commit an output")
	}
}

func TestRunMissingMappingFailsBeforeRemux(t *testing.T) {
	dir := t.TempDir()
	p := fixture.BuildValid(fixture.Options{})
	in := writeTemp(t, dir, "in.mp4", p.Data)
	opts := Options{MappingFor: func(inputs []track.Input) (track.Map, error) {
		return track.New(inputs[:len(inputs)-1], []uint32{1, 2, 3, 4})
	}}
	sum, err := Run(dir, in, filepath.Join(dir, "out.mp4"), opts)
	if err == nil {
		t.Fatal("expected mapping failure")
	}
	if sum.ErrorStage != "map" {
		t.Fatalf("stage: %+v", sum)
	}
}

func TestRecoverIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	p, _ := fixture.BuildRecoverable(fixture.Options{})
	in := writeTemp(t, dir, "recoverable.mp4", p.Data)
	out := filepath.Join(dir, "out.mp4")
	first, err := Recover(dir, in, out)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Recover(dir, in, out)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Committed || !second.Committed {
		t.Fatalf("commit flags: %+v %+v", first, second)
	}
	if first.PayloadSHA256In != first.PayloadSHA256Out {
		t.Fatalf("payload changed: %+v", first)
	}
	if second.PayloadSHA256Out != first.PayloadSHA256Out || second.DuplicateSegments != 0 {
		t.Fatalf("second run not idempotent: %+v", second)
	}
	if second.CommittedSegments != first.CommittedSegments {
		t.Fatalf("committed boundaries differ: %d vs %d", first.CommittedSegments, second.CommittedSegments)
	}
}
