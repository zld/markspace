package container

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/core"
	"mediacore/internal/fixture"
)

func writeFixture(t *testing.T, kind fixture.Kind) string {
	t.Helper()
	m := fixture.GeneratePCM(fixture.DefaultPlan(kind))
	b, err := fixture.Build(m)
	if err != nil {
		t.Fatalf("build %s: %v", kind, err)
	}
	path := filepath.Join(t.TempDir(), string(kind)+".mp4")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScanCleanFixtureFindsAudioTrack(t *testing.T) {
	s, err := ScanFile(writeFixture(t, fixture.KindClean), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", s.Warnings)
	}
	if len(s.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(s.Tracks))
	}
	tr := s.Tracks[0]
	if !tr.IsAudio || tr.Format.Codec != "mp4a" || tr.Format.SampleRate != 44100 || tr.Format.Channels != 2 {
		t.Fatalf("bad track: %+v", tr)
	}
	if !tr.IndexComplete {
		t.Fatalf("clean index marked incomplete: %v", tr.Warnings)
	}
	if tr.SampleCount() == 0 || len(tr.Offsets) == 0 {
		t.Fatalf("missing samples: %+v", tr)
	}
	if s.FTyp != "M4A " {
		t.Fatalf("ftyp = %q", s.FTyp)
	}
}

func TestScanTruncatedFileKeepsReadablePrefix(t *testing.T) {
	path := writeFixture(t, fixture.KindTruncated)
	s, err := ScanFile(path, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if s.FileSize >= 100_000 {
		t.Fatalf("fixture was not truncated: %d", s.FileSize)
	}
	// moov precedes mdat, so the index is fully readable even though the
	// media bytes are not.
	if len(s.Tracks) != 1 || !s.Tracks[0].IndexComplete {
		t.Fatalf("index should survive truncation: %+v", s.Tracks)
	}
	// The last chunk offset must point past EOF now; recovery, not the
	// scanner, is responsible for noticing.
	bad := 0
	for _, off := range s.Tracks[0].Offsets {
		if off >= s.FileSize {
			bad++
		}
	}
	if bad == 0 {
		t.Fatal("expected at least one chunk offset beyond EOF")
	}
}

func TestScanWithoutMoovReportsMissingIndex(t *testing.T) {
	s, err := ScanFile(writeFixture(t, fixture.KindNoMoov), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tracks) != 0 {
		t.Fatalf("tracks = %d", len(s.Tracks))
	}
	if !hasCode(s.Warnings, core.CodeMoovMissing) {
		t.Fatalf("want moov_missing, got %v", s.Warnings)
	}
}

func TestScanNoIndexLeavesTrackButIncomplete(t *testing.T) {
	s, err := ScanFile(writeFixture(t, fixture.KindNoIndex), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(s.Tracks))
	}
	tr := s.Tracks[0]
	if tr.IndexComplete {
		t.Fatal("index reported complete")
	}
	if tr.SampleCount() != 0 || len(tr.Offsets) != 0 {
		t.Fatalf("stale index data: %+v", tr)
	}
	if !hasCode(tr.Warnings, core.CodeTableTruncated) {
		t.Fatalf("want table_truncated, got %v", tr.Warnings)
	}
}

func TestScanRejectsAbsurdBoxSizes(t *testing.T) {
	// A box declaring a size beyond EOF must not make the scanner allocate or
	// trust the contents.
	b := make([]byte, 64)
	binary.BigEndian.PutUint32(b[0:], 0xFFFFFFF0)
	copy(b[4:], "moov")
	path := filepath.Join(t.TempDir(), "huge.mp4")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ScanFile(path, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Root.Children) != 1 || !s.Root.Children[0].Truncated {
		t.Fatalf("box not marked truncated: %+v", s.Root.Children)
	}
	if s.Root.Children[0].Size > s.FileSize {
		t.Fatalf("size %d exceeds file %d", s.Root.Children[0].Size, s.FileSize)
	}
	if !hasCode(s.Warnings, core.CodeMoovMissing) {
		t.Fatalf("want moov warning, got %v", s.Warnings)
	}
}

func TestScanRejectsGarbageBoxType(t *testing.T) {
	b := make([]byte, 32)
	binary.BigEndian.PutUint32(b[0:], 24)
	copy(b[4:], "\x00\x01\x02\x03")
	path := filepath.Join(t.TempDir(), "garbage.mp4")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ScanFile(path, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Root.Children) != 0 {
		t.Fatalf("implausible box accepted: %+v", s.Root.Children)
	}
}

func TestScanEnforcesLimits(t *testing.T) {
	path := writeFixture(t, fixture.KindClean)
	lim := DefaultLimits()
	lim.MaxFileBytes = 100
	if _, err := ScanFile(path, lim); !core.IsCode(err, core.CodeLimitExceeded) {
		t.Fatalf("want limit error, got %v", err)
	}

	lim = DefaultLimits()
	lim.MaxBoxes = 2
	s, err := ScanFile(path, lim)
	if err != nil {
		t.Fatal(err)
	}
	if s.BoxCount > 3 {
		t.Fatalf("box limit ignored: %d", s.BoxCount)
	}
	if !hasCode(s.Warnings, core.CodeLimitExceeded) {
		t.Fatalf("want limit warning, got %v", s.Warnings)
	}
}

func TestScanMissingFileIsIOError(t *testing.T) {
	_, err := ScanFile(filepath.Join(t.TempDir(), "absent.mp4"), DefaultLimits())
	if !core.IsCode(err, core.CodeIO) {
		t.Fatalf("want io error, got %v", err)
	}
}

func hasCode(errs []*core.Error, code core.Code) bool {
	for _, e := range errs {
		if e.Code == code {
			return true
		}
	}
	return false
}
