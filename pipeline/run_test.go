package pipeline_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mp4 "github.com/abema/go-mp4"

	"mediacheck/audio"
	"mediacheck/color"
	"mediacheck/internal/fixture"
	"mediacheck/pipeline"
)

func healthyConfig(t *testing.T, out string) pipeline.Config {
	t.Helper()
	return pipeline.Config{
		IndexDir:         filepath.Join(t.TempDir(), "idx"),
		OutputPath:       out,
		Frames:           48,
		FPS:              24,
		TargetSampleRate: 48000,
		ColorPolicy:      color.Policy{PreferTrack: true},
		AudioPolicy:      audio.Config{FallbackLayout: "stereo"},
		RTPWindow:        64,
		RTPLossRate:      0.05,
		RTPJitterMs:      8,
		RTPHardFraction:  0.05,
	}
}

func TestRunHealthyPublishesAndMeetsLimits(t *testing.T) {
	stream, err := fixture.Build(fixture.Config{Seed: 17, Frames: 48})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.mp4")
	cfg := healthyConfig(t, out)
	m, err := pipeline.Run(context.Background(), bytes.NewReader(stream), 17, cfg)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !m.Published {
		t.Fatalf("healthy run must publish, faults: %v", m.Faults)
	}
	if m.InputSHA256 == "" || len(m.FrameHashes) != 48 {
		t.Fatalf("report fields missing: %d hashes", len(m.FrameHashes))
	}
	for _, h := range m.FrameHashes {
		if h == "" {
			t.Fatal("healthy run must hash every frame")
		}
	}
	if m.AVSyncErrorMs > 20 {
		t.Fatalf("av sync %dms exceeds 20ms", m.AVSyncErrorMs)
	}
	if m.RecoveryRate < 0.9 {
		t.Fatalf("recovery rate %f below 0.9", m.RecoveryRate)
	}
	if m.P95LatencyMs > 50 {
		t.Fatalf("p95 latency %fms exceeds 50ms", m.P95LatencyMs)
	}
	if m.AudioSamplesIn <= 0 || m.AudioSamplesOut <= 0 {
		t.Fatal("audio sample counts not reported")
	}
	if _, err := os.Stat(m.CompletionMarker); err != nil {
		t.Fatalf("completion marker missing: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("mp4 output missing: %v", err)
	}
	assertValidMP4(t, out)
}

func TestRunDeterministicAcrossRuns(t *testing.T) {
	stream, err := fixture.Build(fixture.Config{Seed: 17, Frames: 32})
	if err != nil {
		t.Fatal(err)
	}
	cfg1 := healthyConfig(t, filepath.Join(t.TempDir(), "a.mp4"))
	m1, err := pipeline.Run(context.Background(), bytes.NewReader(stream), 17, cfg1)
	if err != nil {
		t.Fatal(err)
	}
	cfg2 := healthyConfig(t, filepath.Join(t.TempDir(), "b.mp4"))
	m2, err := pipeline.Run(context.Background(), bytes.NewReader(stream), 17, cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if m1.InputSHA256 != m2.InputSHA256 {
		t.Fatal("input summary differs across runs")
	}
	if len(m1.FrameHashes) != len(m2.FrameHashes) {
		t.Fatal("frame hash length differs")
	}
	for i := range m1.FrameHashes {
		if m1.FrameHashes[i] != m2.FrameHashes[i] {
			t.Fatalf("frame hash %d differs: %s vs %s", i, m1.FrameHashes[i], m2.FrameHashes[i])
		}
	}
}

func TestRunIndexReusedOnSecondRun(t *testing.T) {
	stream, err := fixture.Build(fixture.Config{Seed: 17, Frames: 16})
	if err != nil {
		t.Fatal(err)
	}
	cfg := healthyConfig(t, filepath.Join(t.TempDir(), "a.mp4"))
	cfg.Frames = 16
	first, err := pipeline.Run(context.Background(), bytes.NewReader(stream), 17, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !first.IndexRebuilt {
		t.Fatal("first run must build the index")
	}
	second, err := pipeline.Run(context.Background(), bytes.NewReader(stream), 17, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if second.IndexRebuilt {
		t.Fatal("second run with identical input must reuse the index")
	}
}

func TestRunTruncatedDoesNotPublish(t *testing.T) {
	stream, err := fixture.Build(fixture.Config{Seed: 17, Frames: 48})
	if err != nil {
		t.Fatal(err)
	}
	cut, off, err := fixture.TruncateAtCluster(stream, 40)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bad.mp4")
	cfg := healthyConfig(t, out)
	m, err := pipeline.Run(context.Background(), bytes.NewReader(cut), 17, cfg)
	if err != nil {
		t.Fatalf("corruption is a soft report, not hard error: %v", err)
	}
	if m.Published {
		t.Fatal("truncated input must never publish a completion result")
	}
	// The scan reports the element boundary where it hit EOF, which can sit a
	// few bytes before the physical cut; both must land inside the same cluster.
	if m.TruncatedAt <= 0 || m.TruncatedAt > off {
		t.Fatalf("truncation offset %d not within (0,%d]", m.TruncatedAt, off)
	}
	if _, err := os.Stat(out + ".complete"); !os.IsNotExist(err) {
		t.Fatal("completion marker must not exist for truncated input")
	}
	if len(m.Faults) == 0 {
		t.Fatal("corrupt run must record faults")
	}
}

func TestRunBadAudioLayoutReported(t *testing.T) {
	stream, err := fixture.Build(fixture.Config{Seed: 17, Frames: 12, BadLayout: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg := healthyConfig(t, filepath.Join(t.TempDir(), "bad.mp4"))
	cfg.Frames = 12
	m, err := pipeline.Run(context.Background(), bytes.NewReader(stream), 17, cfg)
	if err != nil {
		t.Fatalf("layout faults are soft report errors: %v", err)
	}
	found := false
	for _, f := range m.Faults {
		if strings.Contains(f, "audio layout") {
			found = true
		}
	}
	if !found {
		t.Fatalf("duplicate layout must be recorded as a fault: %v", m.Faults)
	}
	if m.Published {
		t.Fatal("invalid layout must block publication")
	}
}

func TestRunUnsafeColorBlocksPublish(t *testing.T) {
	stream, err := fixture.Build(fixture.Config{
		Seed: 17, Frames: 12, ColorConflict: true, UnknownColor: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := healthyConfig(t, filepath.Join(t.TempDir(), "c.mp4"))
	cfg.Frames = 12
	m, err := pipeline.Run(context.Background(), bytes.NewReader(stream), 17, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if m.ColorUnsafeReason == "" {
		t.Fatal("unknown colour enumeration must be flagged unsafe")
	}
	if m.Published {
		t.Fatal("unsafe colour must block publication")
	}
}

func assertValidMP4(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	seen := map[string]bool{}
	containers := map[string]bool{
		"moov": true, "trak": true, "mdia": true, "minf": true,
		"stbl": true, "dinf": true, "dref": true,
	}
	var handler mp4.ReadHandler
	handler = func(h *mp4.ReadHandle) (interface{}, error) {
		seen[h.BoxInfo.Type.String()] = true
		if containers[h.BoxInfo.Type.String()] {
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(f, handler); err != nil {
		t.Fatalf("remuxed mp4 is not parseable: %v", err)
	}
	for _, want := range []string{"ftyp", "moov", "mdat", "stbl", "stco", "stsz", "stts"} {
		if !seen[want] {
			t.Fatalf("remuxed mp4 missing box %q (found %v)", want, seen)
		}
	}
}
