package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hlsforge/internal/fixture"
	"hlsforge/internal/ingest"
	"hlsforge/internal/manifest"
	"hlsforge/internal/report"
	"hlsforge/internal/segment"
	"hlsforge/internal/timeline"
)

// buildPipeline mirrors what the app build command will do, so verify is
// tested against the real on-disk artifact layout.
func buildPipeline(t *testing.T, inbox, hls string, seqs int, faults []string) []manifest.TrackInput {
	t.Helper()
	if _, err := fixture.Generate(inbox, []int{800000, 1600000}, fixture.Options{
		Seed: 7, PartMs: 500, DurationMs: int64(seqs) * 500, Faults: faults,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := ingest.Collect(inbox, 1, seqs, 500)
	if err != nil {
		t.Fatal(err)
	}
	var inputs []manifest.TrackInput
	for _, ct := range res.Tracks {
		tr, err := timeline.Build(ct)
		if err != nil {
			t.Fatal(err)
		}
		layout, err := segment.Layout(tr, 2000, 500, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := segment.Persist(hls, tr, layout); err != nil {
			t.Fatal(err)
		}
		// Copy the init segment next to the media files for EXT-X-MAP.
		initSrc := filepath.Join(ct.TrackDir(), fixture.InitName())
		initDst := filepath.Join(hls, layout.SegmentDir, fixture.InitName())
		raw, err := os.ReadFile(initSrc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(initDst, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, manifest.TrackInput{
			Bitrate: ct.Bitrate, Layout: layout,
			InitURI: filepath.Join(layout.SegmentDir, fixture.InitName()),
			HasPreload: true, CanSkipTo: true,
		})
	}
	if err := manifest.WriteAll(hls, "master.m3u8", inputs); err != nil {
		t.Fatal(err)
	}
	return inputs
}

func hasFinding(fs []report.Finding, code report.Code, bitrate int) bool {
	for _, f := range fs {
		if f.Code == code && (bitrate == 0 || f.Bitrate == bitrate) {
			return true
		}
	}
	return false
}

func TestVerifyCleanBuild(t *testing.T) {
	inbox, hls := t.TempDir(), t.TempDir()
	buildPipeline(t, inbox, hls, 8, nil)
	summaries, findings, err := Verify(Input{
		Inbox: inbox, HLS: hls, FromSeq: 1, ToSeq: 8,
		PartMs: 500, SegmentMs: 2000, ToleranceMs: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("clean build must have no findings: %+v", findings)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries = %d", len(summaries))
	}
	for _, s := range summaries {
		if s.Segments != 2 || s.Parts != 8 || s.Fragments != 8 {
			t.Fatalf("summary counters wrong: %+v", s)
		}
	}
}

func TestVerifyDetectsDriftAndMissing(t *testing.T) {
	inbox, hls := t.TempDir(), t.TempDir()
	buildPipeline(t, inbox, hls, 8, []string{"missing", "drift"})
	summaries, findings, err := Verify(Input{
		Inbox: inbox, HLS: hls, FromSeq: 1, ToSeq: 8,
		PartMs: 500, SegmentMs: 2000, ToleranceMs: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(findings, report.CodeTimelineDrift, 800000) {
		t.Fatalf("drift finding missing: %+v", findings)
	}
	if !hasFinding(findings, report.CodeMissingPart, 1600000) {
		t.Fatalf("missing-part finding missing: %+v", findings)
	}
	for _, s := range summaries {
		switch s.BitrateKbps {
		case 800:
			if s.DeviationMs != fixture.DriftMs {
				t.Fatalf("800k deviation = %d, want %d", s.DeviationMs, fixture.DriftMs)
			}
		case 1600:
			if s.Fragments != 7 {
				t.Fatalf("1600k fragments = %d, want 7", s.Fragments)
			}
			if s.DeviationMs != 0 {
				t.Fatalf("reference 1600k deviation = %d, want 0", s.DeviationMs)
			}
		}
	}
}

func TestVerifyDetectsHashMismatch(t *testing.T) {
	inbox, hls := t.TempDir(), t.TempDir()
	buildPipeline(t, inbox, hls, 8, nil)
	// Corrupt one part file on disk after the build.
	target := filepath.Join(hls, "800000", "seg-00000-part-0.mp4")
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xFF
	if err := os.WriteFile(target, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, findings, err := Verify(Input{
		Inbox: inbox, HLS: hls, FromSeq: 1, ToSeq: 8,
		PartMs: 500, SegmentMs: 2000, ToleranceMs: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(findings, report.CodeHashMismatch, 800000) {
		t.Fatalf("hash mismatch finding missing: %+v", findings)
	}
}

func TestVerifyDetectsManifestTampering(t *testing.T) {
	inbox, hls := t.TempDir(), t.TempDir()
	buildPipeline(t, inbox, hls, 8, nil)
	pl := filepath.Join(hls, "800000.m3u8")
	raw, err := os.ReadFile(pl)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "#EXT-X-TARGETDURATION:") {
			lines[i] = "#EXT-X-TARGETDURATION:99"
			break
		}
	}
	tampered := []byte(strings.Join(lines, "\n"))
	if err := os.WriteFile(pl, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	_, findings, err := Verify(Input{
		Inbox: inbox, HLS: hls, FromSeq: 1, ToSeq: 8,
		PartMs: 500, SegmentMs: 2000, ToleranceMs: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	var diff *report.Finding
	for i := range findings {
		if findings[i].Code == report.CodeManifestDiff {
			diff = &findings[i]
		}
	}
	if diff == nil {
		t.Fatalf("manifest mismatch finding missing: %+v", findings)
	}
	if diff.Bitrate != 800000 || diff.Field != "EXT-X-TARGETDURATION" {
		t.Fatalf("finding must name bitrate and field: %+v", diff)
	}
}

func TestVerifyDetectsMissingFile(t *testing.T) {
	inbox, hls := t.TempDir(), t.TempDir()
	buildPipeline(t, inbox, hls, 8, nil)
	if err := os.Remove(filepath.Join(hls, "1600000", "seg-00001-part-2.mp4")); err != nil {
		t.Fatal(err)
	}
	_, findings, err := Verify(Input{
		Inbox: inbox, HLS: hls, FromSeq: 1, ToSeq: 8,
		PartMs: 500, SegmentMs: 2000, ToleranceMs: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(findings, report.CodeMissingFile, 1600000) {
		t.Fatalf("missing-file finding absent: %+v", findings)
	}
}
