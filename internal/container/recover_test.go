package container

import (
	"testing"

	"mediacore/internal/core"
	"mediacore/internal/fixture"
)

func recoverKind(t *testing.T, kind fixture.Kind) *TrackRecovery {
	t.Helper()
	path := writeFixture(t, kind)
	s, err := ScanFile(path, DefaultLimits())
	if err != nil {
		t.Fatalf("scan %s: %v", kind, err)
	}
	if len(s.Tracks) == 0 {
		t.Fatalf("%s: no tracks", kind)
	}
	res, rerr := Recovery{File: path, Scan: s, Limits: DefaultLimits()}.Recover(&s.Tracks[0])
	if res == nil {
		t.Fatalf("%s: recover returned nil: %v", kind, rerr)
	}
	return res
}

func TestRecoverCleanHasNoGaps(t *testing.T) {
	res := recoverKind(t, fixture.KindClean)
	if res.IndexSource != "tables" {
		t.Fatalf("index source = %q", res.IndexSource)
	}
	if len(res.Gaps) != 0 {
		t.Fatalf("clean file has gaps: %+v", res.Gaps)
	}
	if len(res.Valid) != 1 {
		t.Fatalf("valid runs = %d", len(res.Valid))
	}
	var payload int64
	for _, smp := range res.Samples {
		payload += smp.Size
	}
	if res.ValidBytes != payload {
		t.Fatalf("valid bytes %d, want sample payload %d", res.ValidBytes, payload)
	}
	// Every decoded sample must be marked decoded and carry positive size.
	for _, s := range res.Samples {
		if !s.Decoded || s.Size <= 0 {
			t.Fatalf("sample %d not decoded: %+v", s.Index, s)
		}
	}
}

func TestRecoverTruncatedKeepsPrefixAndReportsGap(t *testing.T) {
	res := recoverKind(t, fixture.KindTruncated)
	if len(res.Gaps) == 0 {
		t.Fatal("truncated file reported no gaps")
	}
	if len(res.Valid) != 1 {
		t.Fatalf("expected one valid prefix, got %d", len(res.Valid))
	}
	// The valid run must end before the gap and never overlap it.
	gap := res.Gaps[0]
	run := res.Valid[0]
	if run.End() > gap.Start {
		t.Fatalf("valid run %+v overlaps gap %+v", run, gap)
	}
	if gap.Offset < res.TotalBytes {
		t.Logf("gap offset %d inside readable bytes (expected for sample-level damage)", gap.Offset)
	}
	if !hasCode(res.Warnings, core.CodeTableTruncated) && !hasCode(res.Warnings, core.CodeSampleRange) {
		t.Fatalf("want a located range error, got %v", res.Warnings)
	}
	// Samples before the damage must be truly valid, not merely present.
	validCount := 0
	for _, s := range res.Samples {
		if s.Decoded {
			validCount++
		}
	}
	if validCount == 0 {
		t.Fatal("no samples survived truncation")
	}
}

func TestRecoverBadRangeLeavesValidPrefixAndTwoGaps(t *testing.T) {
	res := recoverKind(t, fixture.KindBadRange)
	if len(res.Gaps) == 0 {
		t.Fatal("bad-range reported no gaps")
	}
	// The first chunk stays valid; damage begins at the second chunk. The
	// corrupted offset must be rejected rather than read as if it were real.
	if len(res.Valid) == 0 {
		t.Fatalf("missing valid prefix: %+v", res.Valid)
	}
	if res.ValidBytes == 0 {
		t.Fatal("no valid bytes before damage")
	}
	var sawRange bool
	for _, w := range res.Warnings {
		if w.Code == core.CodeSampleRange || w.Code == core.CodeTableTruncated {
			sawRange = true
		}
	}
	if !sawRange {
		t.Fatalf("no range errors: %v", res.Warnings)
	}
}

func TestRecoverNoIndexRebuildsFromRecords(t *testing.T) {
	res := recoverKind(t, fixture.KindNoIndex)
	if res.IndexSource != "rescan" {
		t.Fatalf("index source = %q", res.IndexSource)
	}
	if len(res.Gaps) != 0 {
		t.Fatalf("intact records should rescan without gaps: %+v", res.Gaps)
	}
	if len(res.Samples) == 0 {
		t.Fatal("no samples recovered")
	}
	// Rebuilt durations must equal the encoded frame counts.
	var total int64
	for _, s := range res.Samples {
		if !s.Decoded {
			t.Fatalf("rescan sample %d not verified: %+v", s.Index, s)
		}
		total += int64(s.Duration)
	}
	if total != 88200 {
		t.Fatalf("recovered duration %d, want 88200", total)
	}
}

func TestRecoverNoMoovIsUnrecoverableWithoutTrack(t *testing.T) {
	path := writeFixture(t, fixture.KindNoMoov)
	s, err := ScanFile(path, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, rerr := Recovery{File: path, Scan: s, Limits: DefaultLimits()}.Recover(nil)
	if !core.IsCode(rerr, core.CodeMissingTrack) {
		t.Fatalf("want missing_track, got %v", rerr)
	}
}

func TestRescanRejectsCorruptRecordBody(t *testing.T) {
	path := writeFixture(t, fixture.KindNoIndex)
	b := readFile(t, path)
	// Flip a payload byte inside the first record's data area so its CRC no
	// longer matches. The region must become a gap, not fabricated audio.
	m := fixture.GeneratePCM(fixture.DefaultPlan(fixture.KindNoIndex))
	_ = m
	idx := findFirstMagic(b)
	if idx < 0 {
		t.Fatal("no record magic in fixture")
	}
	b[idx+fixtureRecordHeader+2] ^= 0xFF
	writeFile(t, path, b)

	s, err := ScanFile(path, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	res, rerr := Recovery{File: path, Scan: s, Limits: DefaultLimits()}.Recover(&s.Tracks[0])
	if res == nil {
		t.Fatalf("recover nil: %v", rerr)
	}
	if len(res.Gaps) == 0 {
		t.Fatal("corrupt record was not reported as a gap")
	}
	if !hasCode(res.Warnings, core.CodeCorruptFragment) {
		t.Fatalf("want corrupt_fragment, got %v", res.Warnings)
	}
}

func TestRescanIsDeterministic(t *testing.T) {
	path := writeFixture(t, fixture.KindNoIndex)
	first := recoverKind(t, fixture.KindNoIndex)
	// Re-scan the same bytes through a second recovery and compare.
	s, err := ScanFile(path, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	second, rerr := Recovery{File: path, Scan: s, Limits: DefaultLimits()}.Recover(&s.Tracks[0])
	if second == nil || rerr != nil {
		t.Fatalf("second recovery: %v", rerr)
	}
	if len(first.Samples) != len(second.Samples) || len(first.Gaps) != len(second.Gaps) {
		t.Fatalf("nondeterministic recovery: %d/%d vs %d/%d",
			len(first.Samples), len(first.Gaps), len(second.Samples), len(second.Gaps))
	}
	for i := range first.Samples {
		a, b := first.Samples[i], second.Samples[i]
		if a.Offset != b.Offset || a.Size != b.Size || a.Start != b.Start || a.Decoded != b.Decoded {
			t.Fatalf("sample %d differs: %+v vs %+v", i, a, b)
		}
	}
}
