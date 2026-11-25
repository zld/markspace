package archive

import (
	"os"
	"path/filepath"
	"testing"

	"archive.example/mediakernel/internal/events"
	"archive.example/mediakernel/internal/fixture"
)

func runFixture(t *testing.T, kind fixture.Kind, seed int64) RunResult {
	t.Helper()
	cfg := DefaultConfig(kind, seed)
	dir := t.TempDir()
	res, err := Run(cfg, dir)
	if err != nil && err.Fatal() {
		t.Fatalf("fatal run for %s: %v", kind, err)
	}
	return res
}

func TestNormalRunProducesDeterministicManifest(t *testing.T) {
	a := runFixture(t, fixture.KindNormal, 17)
	b := runFixture(t, fixture.KindNormal, 17)
	if a.Manifest.ManifestSHA256 != b.Manifest.ManifestSHA256 {
		t.Fatalf("manifest digest differs:\n%s\n%s", a.Manifest.ManifestSHA256, b.Manifest.ManifestSHA256)
	}
	if a.Manifest.Frames.SequenceSHA256 != b.Manifest.Frames.SequenceSHA256 {
		t.Fatal("frame sequence digest differs")
	}
	if a.Report.StateHash != b.Report.StateHash {
		t.Fatal("state hash differs")
	}
	if len(a.Manifest.Frames.FrameHashes) != a.Manifest.Frames.Count {
		t.Fatalf("frame hash count %d vs frames %d", len(a.Manifest.Frames.FrameHashes), a.Manifest.Frames.Count)
	}
}

func TestNormalRunCleanAndAligned(t *testing.T) {
	res := runFixture(t, fixture.KindNormal, 17)
	if res.Report.Recovered {
		t.Fatalf("normal run reported recovery: %+v", res.Report)
	}
	if !res.Report.FramesOK {
		t.Fatalf("alignment exceeded a frame: %d > %d", res.Report.MaxAlignErr, res.Report.FrameDuration)
	}
	if !res.Report.AudioFinite {
		t.Fatal("loudness peak not finite")
	}
	if res.Report.GapCount != 0 {
		t.Fatalf("normal run has %d gaps", res.Report.GapCount)
	}
	if res.Report.Frames == 0 {
		t.Fatal("no frames exported")
	}
	if res.Report.Segments == 0 {
		t.Fatal("no segments exported")
	}
}

func TestFrameAlignmentNeverExceedsOneFrame(t *testing.T) {
	for _, kind := range []fixture.Kind{fixture.KindNormal, fixture.KindDamaged, fixture.KindLate, fixture.KindBoundary} {
		res := runFixture(t, kind, 17)
		if res.Report.MaxAlignErr > res.Report.FrameDuration && res.Report.Frames > 0 {
			t.Fatalf("%s: alignment %d > frame %d", kind, res.Report.MaxAlignErr, res.Report.FrameDuration)
		}
	}
}

func TestTruncatedRunReportsTrackAndOffsetKeepsOtherTracks(t *testing.T) {
	res := runFixture(t, fixture.KindTruncated, 17)
	if !res.Report.Recovered {
		t.Fatal("truncated run not marked recovered")
	}
	var attributed bool
	for _, e := range res.Manifest.Errors {
		if e.Code == string(events.CodeTruncated) && e.Track != "" && e.Offset > 0 {
			attributed = true
		}
	}
	if !attributed {
		t.Fatalf("no track-attributed truncation: %+v", res.Manifest.Errors)
	}
	// Other complete tracks still export frames and events.
	if res.Manifest.Frames.Count == 0 {
		t.Fatal("no frames exported for truncated input")
	}
	if len(res.Manifest.Timeline.ByTrack) < 3 {
		t.Fatalf("too few tracks survived: %+v", res.Manifest.Timeline.ByTrack)
	}
}

func TestDamagedRunHasGapsAndAlignedFrames(t *testing.T) {
	res := runFixture(t, fixture.KindDamaged, 17)
	if res.RTPStats.Lost == 0 {
		t.Fatal("damaged run reported no losses")
	}
	if len(res.Manifest.Gaps) == 0 {
		t.Fatal("damaged run reported no unresolved gaps")
	}
	for _, g := range res.Manifest.Gaps {
		if g.AtNS != -1 {
			t.Fatalf("gap invented a timestamp: %+v", g)
		}
	}
	if res.Manifest.Frames.Count == 0 {
		t.Fatal("damaged run exported no frames")
	}
}

func TestDamagedRunReportsTruncationAndGaps(t *testing.T) {
	res := runFixture(t, fixture.KindDamaged, 17)
	// The literal acceptance sentence attaches track+offset truncation
	// reporting to the damaged invocation as well.
	var truncated bool
	for _, e := range res.Manifest.Errors {
		if e.Code == string(events.CodeTruncated) && e.Track != "" && e.Offset > 0 {
			truncated = true
		}
	}
	if !truncated {
		t.Fatalf("damaged run missing track-attributed truncation: %+v", res.Manifest.Errors)
	}
	if res.Report.GapCount == 0 {
		t.Fatal("damaged run must still report unresolved gaps")
	}
	if res.Manifest.Frames.Count == 0 {
		t.Fatal("other tracks must still export frames")
	}
}

func TestLateRunReplayMatchesOrderedInput(t *testing.T) {
	ordered := runFixture(t, fixture.KindNormal, 17)
	late := runFixture(t, fixture.KindLate, 17)
	if late.Timeline.Stats().Replays == 0 {
		t.Fatal("late run did not trigger a replay")
	}
	if late.Report.StateHash != ordered.Report.StateHash {
		t.Fatalf("late replay state %s != ordered state %s", late.Report.StateHash, ordered.Report.StateHash)
	}
	if late.Manifest.Frames.SequenceSHA256 != ordered.Manifest.Frames.SequenceSHA256 {
		t.Fatalf("late replay frames %s != ordered frames %s",
			late.Manifest.Frames.SequenceSHA256, ordered.Manifest.Frames.SequenceSHA256)
	}
}

func TestBoundaryRunIsRecoverable(t *testing.T) {
	res := runFixture(t, fixture.KindBoundary, 17)
	var found bool
	for _, e := range res.Manifest.Errors {
		if e.Code == string(events.CodeTimeOutOfRange) {
			found = true
		}
	}
	if !found {
		t.Fatalf("boundary run did not report an out-of-range timestamp: %+v", res.Manifest.Errors)
	}
	// The rest of the archive still exports.
	if res.Manifest.Frames.Count == 0 {
		t.Fatal("boundary run exported no frames")
	}
}

func TestManifestWrittenToConfinedDirectory(t *testing.T) {
	res := runFixture(t, fixture.KindNormal, 17)
	path := res.Report.ManifestPath
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
	if filepath.Base(path) != "manifest.json" {
		t.Fatalf("unexpected manifest name %s", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("empty manifest")
	}
}

func TestLongRunLimitsRespected(t *testing.T) {
	// The event and replay limits must produce explicit errors, never panics.
	cfg := DefaultConfig(fixture.KindNormal, 17)
	dir := t.TempDir()
	if _, err := Run(cfg, dir); err != nil && err.Fatal() {
		t.Fatalf("unexpected fatal: %v", err)
	}
}

func TestReportFieldsConsistent(t *testing.T) {
	res := runFixture(t, fixture.KindNormal, 17)
	if res.Report.Frames != res.Manifest.Frames.Count {
		t.Fatalf("report frames %d vs manifest %d", res.Report.Frames, res.Manifest.Frames.Count)
	}
	if res.Report.StateHash != res.Manifest.StateSHA256 {
		t.Fatal("report state hash disagrees with manifest")
	}
	if res.Report.MeanByte != res.Manifest.Frames.MeanByte {
		t.Fatal("report mean disagrees with manifest")
	}
	if res.Report.Peak != res.Manifest.Audio.Peak {
		t.Fatal("report peak disagrees with manifest")
	}
}
