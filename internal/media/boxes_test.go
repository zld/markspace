package media

import (
	"path/filepath"
	"testing"

	"replay/internal/clock"
	"replay/internal/testkit"
)

// fixtureDir is where the shared test fixtures are generated once.
func fixtureDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fx")
	if err := testkit.Generate(dir); err != nil {
		t.Fatalf("generate fixtures: %v", err)
	}
	return dir
}

func TestParseCleanContainer(t *testing.T) {
	dir := fixtureDir(t)
	fi, err := ParseFile(filepath.Join(dir, "clean.mp4"))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if fi.Moov.Type != "moov" || fi.Mdat.Type != "mdat" {
		t.Fatalf("missing moov/mdat refs: %+v", fi.TopLevel)
	}
	if len(fi.Tracks) != 1 {
		t.Fatalf("want 1 track, got %d", len(fi.Tracks))
	}
	track := fi.Tracks[0]
	if track.Timescale != testkit.FixtureTimescale {
		t.Fatalf("timescale=%d want %d", track.Timescale, testkit.FixtureTimescale)
	}
	if track.Handler != HandlerVideo {
		t.Fatalf("handler=%q want vide", track.Handler)
	}
	if track.Stts == nil || track.Stsc == nil || track.Stsz == nil || track.Co64 == nil {
		t.Fatal("missing required sample tables")
	}
	if track.StsdEntry == nil || track.StsdEntry.Type != "avc1" {
		t.Fatalf("stsd entry wrong: %+v", track.StsdEntry)
	}
}

func TestTruncatedContainerReportsOffset(t *testing.T) {
	dir := fixtureDir(t)
	_, err := ParseFile(filepath.Join(dir, "truncated.mp4"))
	if err == nil {
		t.Fatal("expected truncation error, got nil")
	}
	ce, ok := clock.AsClockError(err)
	if !ok {
		t.Fatalf("want ClockError, got %T: %v", err, err)
	}
	if ce.Kind != clock.KindBoxTruncated {
		t.Fatalf("kind=%s want box_truncated", ce.Kind)
	}
	if ce.Offset < 0 {
		t.Fatalf("expected non-negative byte offset, got %d", ce.Offset)
	}
}

func TestBuildTimelineNativeTimeBase(t *testing.T) {
	dir := fixtureDir(t)
	fi, err := ParseFile(filepath.Join(dir, "clean.mp4"))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	tl, err := Build(fi)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	v := tl.Video
	if v == nil || len(v.Samples) != testkit.FrameCount() {
		t.Fatalf("sample count wrong: %d", len(v.Samples))
	}
	// DTS must be derived from stts deltas, not an average frame rate.
	if v.Samples[1].DTS != int64(testkit.FrameDelta()) {
		t.Fatalf("second sample DTS=%d want %d", v.Samples[1].DTS, testkit.FrameDelta())
	}
	if len(v.SyncIdx) != testkit.FrameCount()/testkit.KeyframeEvery() {
		t.Fatalf("sync sample count=%d", len(v.SyncIdx))
	}
	if got := v.SyncAtOrBefore(int64(testkit.FrameDelta()) * 5); got < 0 {
		t.Fatal("expected a sync sample backing t=5s")
	}
	if len(v.Anomalies) != 0 {
		t.Fatalf("clean timeline must have no anomalies, got %d", len(v.Anomalies))
	}
}

func TestGapTimelineReportsAnomaly(t *testing.T) {
	dir := fixtureDir(t)
	fi, err := ParseFile(filepath.Join(dir, "gap.mp4"))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	tl, err := Build(fi)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(tl.Video.Anomalies) != 1 {
		t.Fatalf("want 1 anomaly, got %d: %+v", len(tl.Video.Anomalies), tl.Video.Anomalies)
	}
	a := tl.Video.Anomalies[0]
	if a.Kind != AnomalyGap || a.EndTick <= a.StartTick {
		t.Fatalf("bad anomaly interval: %+v", a)
	}
}
