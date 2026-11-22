package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"mediatimeline/internal/clock"
	"mediatimeline/internal/container"
)

func baseCfg() Config {
	return Config{
		Seed: 42, LateMS: 0, CheckpointEvery: 128,
		InRate: 44100, OutRate: 48000, Channels: 2,
		TargetDBFS: -20, Ceiling: 0.95, WindowFrames: 512,
	}
}

// buildEvents renders a fixture MP4 and returns its scanned events plus a
// reader over the source bytes.
func buildEvents(t *testing.T, spec container.FixtureSpec) ([]container.SampleEvent, *os.File) {
	t.Helper()
	data, err := container.BuildMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "src.m4a")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := container.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tracks, err := sc.ScanTracks()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Close(); f.Close() })
	return tracks[0].Entries, f
}

func TestRunDeterministicOutput(t *testing.T) {
	cfg := baseCfg()
	spec := container.FixtureSpec{
		Seed: 42, Timescale: cfg.InRate, SampleCount: 24,
		FramesPerSamp: 512, Channels: 2, VFRJitter: 8, DriftPPM: 250,
	}
	events, src := buildEvents(t, spec)

	runOnce := func(dir string) Metrics {
		p, err := New(cfg, dir, filepath.Join(dir, "out.pcm"))
		if err != nil {
			t.Fatal(err)
		}
		p.SetSource(src)
		res, err := p.Run(events, 0)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
		return res.Metrics
	}
	a := runOnce(t.TempDir())
	b := runOnce(t.TempDir())
	if a.OutputSHA256 == "" || a.OutputSHA256 != b.OutputSHA256 {
		t.Fatalf("non-deterministic checksum: %s vs %s", a.OutputSHA256, b.OutputSHA256)
	}
	if a.EventCount != int64(len(events)) || a.ResampleFrames == 0 {
		t.Fatalf("metrics wrong: %+v", a)
	}
}

func TestLateReplayAndDedup(t *testing.T) {
	cfg := baseCfg()
	cfg.LateMS = 80
	spec := container.FixtureSpec{
		Seed: 7, Timescale: cfg.InRate, SampleCount: 20,
		FramesPerSamp: 256, Channels: 2, VFRJitter: 6, LateIndices: map[int]bool{},
	}
	events, src := buildEvents(t, spec)
	// Make two samples genuinely late: rewrite their DTS/PTS to an earlier
	// media time, then deliver them out of order behind the running watermark.
	lateShift := uint64(spec.Timescale) / 4 // 250 ms backwards
	markLate := func(idx int) {
		if events[idx].DTSNanos > lateShift {
			events[idx].DTSNanos -= lateShift
			events[idx].PTSNanos = events[idx].DTSNanos
		}
	}
	markLate(2)
	markLate(5)
	// Deliver the two back-dated samples near the end, after the watermark has
	// advanced well past their rewritten media time.
	var shuffled []container.SampleEvent
	shuffled = append(shuffled, events[0], events[1])
	shuffled = append(shuffled, events[3], events[4])
	shuffled = append(shuffled, events[6:]...)
	shuffled = append(shuffled, events[2], events[5])
	// Append a duplicate of event 0 to prove it is counted once.
	shuffled = append(shuffled, events[0])

	p, err := New(cfg, t.TempDir(), filepath.Join(t.TempDir(), "out.pcm"))
	if err != nil {
		t.Fatal(err)
	}
	p.SetSource(src)
	res, err := p.Run(shuffled, 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Metrics.LateEvents < 2 {
		t.Fatalf("late events not detected: %+v", res.Metrics)
	}
	if res.Metrics.SkippedDup != 1 {
		t.Fatalf("duplicate not suppressed: %+v", res.Metrics)
	}
	// All 20 unique samples are processed exactly once (18 on-time path plus
	// the 2 back-dated samples delivered through deterministic replay).
	uniqueProcessed := res.Metrics.EventCount
	if uniqueProcessed != int64(len(events)) {
		t.Fatalf("unique processed=%d want %d", uniqueProcessed, len(events))
	}
}

func TestCRCRecovery(t *testing.T) {
	cfg := baseCfg()
	spec := container.FixtureSpec{
		Seed: 9, Timescale: cfg.InRate, SampleCount: 12,
		FramesPerSamp: 256, Channels: 2,
	}
	data, err := container.BuildMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	corrupt, _, err := container.CorruptCRC(data, 5)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.m4a")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	sc, _ := container.Open(path)
	tracks, err := sc.ScanTracks()
	if err != nil {
		t.Fatal(err)
	}
	// Build a known-good reference and attach its CRCs as the trusted manifest.
	cleanDir := t.TempDir()
	cleanPath := filepath.Join(cleanDir, "clean.m4a")
	if err := os.WriteFile(cleanPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	csc, _ := container.Open(cleanPath)
	cleanTracks, err := csc.ScanTracks()
	if err != nil {
		t.Fatal(err)
	}
	container.AttachTrustedCRC(tracks[0].Entries, cleanTracks[0].Entries)
	src, _ := os.Open(path)
	t.Cleanup(func() { sc.Close(); src.Close(); csc.Close() })

	p, err := New(cfg, t.TempDir(), filepath.Join(t.TempDir(), "out.pcm"))
	if err != nil {
		t.Fatal(err)
	}
	p.SetSource(src)
	res, err := p.Run(tracks[0].Entries, 0)
	if err != nil {
		t.Fatalf("recoverable run should not fail: %v", err)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("skipped=%d want 1", len(res.Skipped))
	}
	ce := res.Skipped[0]
	if ce.Code != clock.ErrCRC || ce.SampleIndex != 5 {
		t.Fatalf("localization wrong: %+v", ce)
	}
}

func TestResumeChecksumMatches(t *testing.T) {
	cfg := baseCfg()
	cfg.CheckpointEvery = 4
	spec := container.FixtureSpec{
		Seed: 11, Timescale: cfg.InRate, SampleCount: 18,
		FramesPerSamp: 256, Channels: 2, VFRJitter: 4,
	}
	events, src := buildEvents(t, spec)

	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.pcm")
	// Interrupted run.
	p1, err := New(cfg, filepath.Join(dir, "ck"), outPath)
	if err != nil {
		t.Fatal(err)
	}
	p1.SetSource(src)
	_, err = p1.Run(events, 6) // simulated kill after six events
	if !IsKillAfter(err) {
		t.Fatalf("want kill-after, got %v", err)
	}
	interrupted := p1.Metrics().OutputSHA256

	// Resume on the same events and output file.
	p2, err := New(cfg, filepath.Join(dir, "ck"), outPath)
	if err != nil {
		t.Fatal(err)
	}
	p2.SetSource(src)
	res, err := p2.Run(events, 0)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	// Uninterrupted reference.
	ref, _ := buildEvents(t, spec)
	refSrc, err := os.Open(filepath.Dir(src.Name()) + "/src.m4a")
	_ = refSrc
	p3, err := New(cfg, t.TempDir(), filepath.Join(t.TempDir(), "ref.pcm"))
	if err != nil {
		t.Fatal(err)
	}
	// Reuse the same source bytes for the reference run.
	p3.SetSource(src)
	if _, err := p3.Run(ref, 0); err != nil {
		t.Fatal(err)
	}
	if res.Metrics.OutputSHA256 != p3.Metrics().OutputSHA256 {
		t.Fatalf("resume checksum %s != reference %s",
			res.Metrics.OutputSHA256, p3.Metrics().OutputSHA256)
	}
	if interrupted == res.Metrics.OutputSHA256 && res.Metrics.EventCount <= 6 {
		t.Fatal("resume did not advance past the kill point")
	}
}

// TestDriftRecoversFixtureRate proves the reported ppm tracks the fixture's
// configured node rate offset rather than reporting a meaningless constant.
func TestDriftRecoversFixtureRate(t *testing.T) {
	cfg := baseCfg()
	const wantPPM = 420.0
	spec := container.FixtureSpec{
		Seed: 42, Timescale: cfg.InRate, SampleCount: 240,
		FramesPerSamp: 512, Channels: 2, Wrap32: true,
		DriftPPM: wantPPM, VFRJitter: 10,
	}
	events, src := buildEvents(t, spec)
	p, err := New(cfg, t.TempDir(), filepath.Join(t.TempDir(), "out.pcm"))
	if err != nil {
		t.Fatal(err)
	}
	p.SetSource(src)
	res, err := p.Run(events, 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := res.Metrics.DriftPPM
	if got < wantPPM-5 || got > wantPPM+5 {
		t.Fatalf("drift=%f want %f +/- 5", got, wantPPM)
	}
}

// TestLateReplayDoesNotDisturbDrift verifies replayed late events still
// contribute their true content position to the drift fit, so the ppm estimate
// is unchanged by delivery reordering.
func TestLateReplayDoesNotDisturbDrift(t *testing.T) {
	cfg := baseCfg()
	spec := container.FixtureSpec{
		Seed: 42, Timescale: cfg.InRate, SampleCount: 240,
		FramesPerSamp: 512, Channels: 2, Wrap32: true,
		DriftPPM: 420, VFRJitter: 10,
	}
	events, src := buildEvents(t, spec)
	// Deliver three samples late: rewrite to earlier media time, move to tail.
	shift := uint64(spec.Timescale) * 80 / 1000
	lateIdx := []int{len(events) / 4, len(events) / 2, 3 * len(events) / 4}
	set := map[int]bool{}
	for _, idx := range lateIdx {
		if events[idx].DTSNanos > shift {
			events[idx].DTSNanos -= shift
			events[idx].PTSNanos = events[idx].DTSNanos
		}
		set[idx] = true
	}
	var onTime, late []container.SampleEvent
	for i, e := range events {
		if set[i] {
			late = append(late, e)
		} else {
			onTime = append(onTime, e)
		}
	}
	shuffled := append(onTime, late...)

	p, err := New(cfg, t.TempDir(), filepath.Join(t.TempDir(), "out.pcm"))
	if err != nil {
		t.Fatal(err)
	}
	p.SetSource(src)
	res, err := p.Run(shuffled, 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := res.Metrics.DriftPPM
	if got < 360 || got > 480 {
		t.Fatalf("late delivery skewed drift: ppm=%f", got)
	}
	if res.Metrics.ReplayedEvents != 3 {
		t.Fatalf("replayed=%d want 3", res.Metrics.ReplayedEvents)
	}
}
