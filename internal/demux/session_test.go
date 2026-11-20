package demux

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tsinspect/internal/fixture"
)

func generate(t *testing.T, scenario string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), scenario+".ts")
	if err := fixture.Generate(path, scenario); err != nil {
		t.Fatalf("generate %s: %v", scenario, err)
	}
	return path
}

func parse(t *testing.T, path string) (*Session, int64, int64) {
	t.Helper()
	s, r, err := ParseFile(path, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer r.Close()
	return s, r.Packets(), r.TrailingBytes()
}

func TestSessionBaseline(t *testing.T) {
	path := generate(t, fixture.ScenarioBaseline)
	s, packets, trail := parse(t, path)
	if packets == 0 || trail != 37 {
		t.Fatalf("packets %d, trailing %d", packets, trail)
	}
	if got := s.Tables.Programs(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("programs = %v", got)
	}
	for _, prog := range []uint16{1, 2} {
		if _, ok := s.Tables.PMT(prog); !ok {
			t.Fatalf("program %d has no active PMT", prog)
		}
	}
	if s.Counter.Totals().ContinuityErrs != 1 {
		t.Fatalf("continuity errors = %d", s.Counter.Totals().ContinuityErrs)
	}
}

func TestSessionUpdatesLoss(t *testing.T) {
	path := generate(t, fixture.ScenarioUpdatesLoss)
	s, _, _ := parse(t, path)
	st := s.Tables.Stats()
	if st.PATVersionChanges != 1 || st.PMTVersionChanges != 2 {
		t.Fatalf("version changes PAT=%d PMT=%d", st.PATVersionChanges, st.PMTVersionChanges)
	}
	if st.PendingDiscards != 1 {
		t.Fatalf("pending discards = %d", st.PendingDiscards)
	}
	// The incomplete end-of-stream PAT (version 3) must not supersede v1.
	if v, _ := s.Tables.PATVersion(); v != 1 {
		t.Fatalf("active PAT version = %d, want 1", v)
	}
	tot := s.Counter.Totals()
	if tot.ContinuityErrs == 0 {
		t.Fatal("expected continuity errors in loss scenario")
	}
	if tot.Duplicates != 1 || tot.Discontinuities != 1 {
		t.Fatalf("duplicates %d discontinuities %d", tot.Duplicates, tot.Discontinuities)
	}
	// The long two-packet PMT must have been reassembled with all streams.
	pmt, ok := s.Tables.PMT(1)
	if !ok || len(pmt.Streams) != 3 {
		t.Fatalf("program 1 PMT = %+v, %v", pmt, ok)
	}
}

func TestSessionCorrupt(t *testing.T) {
	path := generate(t, fixture.ScenarioCorrupt)
	s, _, _ := parse(t, path)
	sec := s.SectionStats()
	if sec.CRCErrors == 0 {
		t.Fatal("bad CRC not detected")
	}
	tot := s.Counter.Totals()
	if tot.TransportErrors == 0 {
		t.Fatal("transport error not counted")
	}
	// Recovery: program 2 must still resolve after the bad PMT.
	pmt, ok := s.Tables.PMT(2)
	if !ok || pmt.Version != 1 {
		t.Fatalf("program 2 PMT after recovery = %+v, %v", pmt, ok)
	}
}

func TestSliceProgram(t *testing.T) {
	src := generate(t, fixture.ScenarioUpdatesLoss)
	out := filepath.Join(t.TempDir(), "p1.ts")
	res, err := Slice(SliceRequest{InputPath: src, OutputPath: out, Program: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Packets == 0 || res.Bytes != res.Packets*188 {
		t.Fatalf("result = %+v", res)
	}
	// A slice must be self-describing and parse cleanly.
	s, _, trail := parse(t, out)
	if trail != 0 {
		t.Fatalf("slice trailing bytes = %d", trail)
	}
	pmt, ok := s.Tables.PMT(1)
	if !ok || len(pmt.Streams) != 3 {
		t.Fatalf("sliced PMT = %+v, %v", pmt, ok)
	}
	if _, ok := s.Tables.PMT(2); ok {
		t.Fatal("slice leaked program 2's PMT")
	}
	for _, stat := range s.Counter.PIDStats() {
		if stat.PID == 0x0201 || stat.PID == 0x0202 {
			t.Fatalf("slice contains program 2 PID 0x%04x", stat.PID)
		}
	}
}

func TestSliceMissingProgram(t *testing.T) {
	src := generate(t, fixture.ScenarioBaseline)
	out := filepath.Join(t.TempDir(), "missing.ts")
	_, err := Slice(SliceRequest{InputPath: src, OutputPath: out, Program: 99})
	if !errors.Is(err, ErrProgramNotFound) {
		t.Fatalf("err = %v, want ErrProgramNotFound", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatal("failed slice left an output file")
	}
}

func TestSliceRefusesOverwrite(t *testing.T) {
	src := generate(t, fixture.ScenarioBaseline)
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Slice(SliceRequest{InputPath: src, OutputPath: src, Program: 1})
	if err == nil {
		t.Fatal("expected refusal")
	}
	after, _ := os.ReadFile(src)
	if len(before) != len(after) {
		t.Fatal("input was modified")
	}
}

func TestSlicePendingPMTFails(t *testing.T) {
	// A stream with a PAT but no PMT at all: program exists, table never
	// becomes complete, so slicing must fail without output.
	path := filepath.Join(t.TempDir(), "pat-only.ts")
	if err := os.WriteFile(path, patOnlyStream(), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.ts")
	_, err := Slice(SliceRequest{InputPath: path, OutputPath: out, Program: 1})
	if !errors.Is(err, ErrPMTPending) {
		t.Fatalf("err = %v, want ErrPMTPending", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatal("failed slice left an output file")
	}
}

func TestSliceRenumbersContinuity(t *testing.T) {
	src := generate(t, fixture.ScenarioUpdatesLoss)
	out := filepath.Join(t.TempDir(), "clean.ts")
	res, err := Slice(SliceRequest{InputPath: src, OutputPath: out, Program: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.ContinuityErrs == 0 {
		t.Fatal("expected source continuity gaps to be reported")
	}
	// The written slice must itself be continuity-clean because counters are
	// renumbered, including adaptation-only packets.
	s, _, _ := parse(t, out)
	if got := s.Counter.Totals().ContinuityErrs; got != 0 {
		t.Fatalf("sliced output has %d continuity errors", got)
	}
}
