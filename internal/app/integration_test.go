package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hlsforge/internal/cli"
	"hlsforge/internal/cmaf"
	"hlsforge/internal/fixture"
	"hlsforge/internal/report"
)

// TestAcceptanceScale reproduces the documented demo scale: two bitrates,
// 24 500ms parts each, 2s segments, with drift on the low track and a missing
// fragment on the high track.
func TestAcceptanceScale(t *testing.T) {
	inbox := t.TempDir()
	hls := t.TempDir()
	metas, err := fixture.Generate(inbox, []int{800000, 1600000}, fixture.Options{
		Seed: 7, PartMs: 500, DurationMs: 12000,
		Faults: []string{"missing", "order", "drift"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(metas[0].Present) != 24 || len(metas[1].Present) != 23 {
		t.Fatalf("part counts wrong: %d / %d",
			len(metas[0].Present), len(metas[1].Present))
	}
	build, err := RunBuild(&cli.BuildConfig{
		In: inbox, Out: hls, FromSeq: 1, ToSeq: 24,
		Segment: 2000, Part: 500, Window: 12000,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range build.Tracks {
		if s.Segments != 6 || s.Parts != 24 {
			t.Fatalf("track %d scale wrong: %+v", s.BitrateKbps, s)
		}
	}
	// A verify over the same window must flag drift (800k) and missing (1600k).
	rep, err := RunVerify(&cli.VerifyConfig{
		In: inbox, HLS: hls, FromSeq: 1, ToSeq: 24, Segment: 2000,
		Part: 500, Window: 12000, Duration: 12000, Tolerance: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HasConsistencyFailure() {
		t.Fatal("verify must report a consistency failure for the faulted input")
	}
	var sawDrift, sawMissing bool
	for _, f := range rep.Findings {
		if f.Code == report.CodeTimelineDrift && f.Bitrate == 800000 {
			sawDrift = true
		}
		if f.Code == report.CodeMissingPart && f.Bitrate == 1600000 {
			sawMissing = true
		}
	}
	if !sawDrift || !sawMissing {
		t.Fatalf("expected drift and missing findings, got %+v", rep.Findings)
	}
}

// TestMalformedFragmentIsRejected writes a hostile fragment whose declared box
// size runs past the buffer; ingest must surface a parse error rather than
// panicking or over-allocating.
func TestMalformedFragmentIsRejected(t *testing.T) {
	inbox := t.TempDir()
	metas, err := fixture.Generate(inbox, []int{800000}, fixture.Options{
		Seed: 1, PartMs: 500, DurationMs: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := metas[0].Dir
	// Replace a fragment with a box header claiming far more bytes than exist.
	malformed := []byte{0, 0, 0xff, 0xff, 'm', 'd', 'a', 't', 'x'}
	if err := os.WriteFile(filepath.Join(dir, fixture.FragmentName(1)),
		malformed, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = RunBuild(&cli.BuildConfig{
		In: inbox, Out: t.TempDir(), FromSeq: 1, ToSeq: 2,
		Segment: 2000, Part: 500, Window: 4000,
	})
	if err == nil || !strings.Contains(err.Error(), cmaf.ErrOutOfBounds.Error()) {
		t.Fatalf("want out-of-bounds parse error, got %v", err)
	}
}

// TestOversizedFragmentRejected ensures the per-fragment size cap is enforced
// before any large allocation.
func TestOversizedFragmentRejected(t *testing.T) {
	big := make([]byte, cmaf.MaxFragmentSize+16)
	for i := range big {
		big[i] = 0
	}
	if _, err := cmaf.ParseFragment(big, 1000); err == nil {
		t.Fatal("oversized fragment must be rejected")
	}
}
