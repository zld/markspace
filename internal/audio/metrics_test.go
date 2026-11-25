package audio

import (
	"math"
	"testing"

	"archive.example/mediakernel/internal/events"
)

func testEvents() []events.Event {
	return []events.Event{
		{Track: events.TrackAudio, Kind: events.KindID3, At: 0, Duration: 1_000_000_000, Seq: 1},
		{Track: events.TrackAudio, Kind: events.KindEmsg, At: 2_000_000_000, Duration: 1_000_000_000, Seq: 2},
	}
}

func TestRenderSamplesDeterministic(t *testing.T) {
	cfg := DefaultConfig(0, 3_000_000_000)
	one, s1, err := RenderSamples(cfg, testEvents())
	if err != nil {
		t.Fatal(err)
	}
	two, s2, err := RenderSamples(cfg, events.SortEvents(testEvents()))
	if err != nil {
		t.Fatal(err)
	}
	if s1.SHA256 != s2.SHA256 {
		t.Fatal("PCM digest differs")
	}
	if len(one) != len(two) {
		t.Fatal("PCM length differs")
	}
	for i := range one {
		if one[i] != two[i] {
			t.Fatalf("PCM byte %d differs", i)
		}
	}
}

func TestPeakFiniteAndBounded(t *testing.T) {
	cfg := DefaultConfig(0, 5_000_000_000)
	_, stats, err := RenderSamples(cfg, testEvents())
	if err != nil {
		t.Fatal(err)
	}
	if !PeakWithin(stats) {
		t.Fatalf("peak not finite/bounded: %+v", stats)
	}
	if math.IsNaN(stats.Peak) || math.IsInf(stats.Peak, 0) {
		t.Fatalf("peak %v is not finite", stats.Peak)
	}
	if stats.Peak <= 0 || stats.Peak > 1 {
		t.Fatalf("peak %v outside (0,1]", stats.Peak)
	}
	if math.IsInf(stats.PeakDBFS, 0) || math.IsNaN(stats.PeakDBFS) {
		// A zero peak maps to the floor; the synthesised signal never zero.
		t.Fatalf("peakDBFS not finite: %v", stats.PeakDBFS)
	}
}

func TestSilenceWhenNoEvents(t *testing.T) {
	// With no events the synthetic waveform stays finite but quieter than with
	// an active envelope.
	cfg := DefaultConfig(0, 2_000_000_000)
	_, quiet, err := RenderSamples(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, loud, err := RenderSamples(cfg, testEvents())
	if err != nil {
		t.Fatal(err)
	}
	if !quiet.Finite || !loud.Finite {
		t.Fatal("stats not finite")
	}
	if loud.Peak <= quiet.Peak {
		t.Fatalf("envelope did not raise the peak: quiet=%f loud=%f", quiet.Peak, loud.Peak)
	}
}

func TestSampleCountAndGeometry(t *testing.T) {
	cfg := DefaultConfig(0, 1_000_000_000)
	pcm, stats, err := RenderSamples(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.SampleRate != 48000 || stats.Channels != 2 || stats.BitsPerSample != 16 {
		t.Fatalf("geometry %+v", stats)
	}
	if len(pcm) != 48000*2*2 {
		t.Fatalf("pcm bytes %d", len(pcm))
	}
	if stats.Samples != 48000 {
		t.Fatalf("samples %d", stats.Samples)
	}
}

func TestSampleLimitEnforced(t *testing.T) {
	cfg := DefaultConfig(0, 10_000_000_000)
	cfg.MaxSamples = 1000
	_, _, err := RenderSamples(cfg, nil)
	if err == nil || err.Code != events.CodeCapacity {
		t.Fatalf("expected capacity error, got %v", err)
	}
}

func TestInvalidConfigRejected(t *testing.T) {
	cfg := DefaultConfig(0, 1_000_000_000)
	cfg.SampleRate = 0
	if _, _, err := RenderSamples(cfg, nil); err == nil {
		t.Fatal("zero sample rate accepted")
	}
	cfg = DefaultConfig(1_000_000_000, 0)
	if _, _, err := RenderSamples(cfg, nil); err == nil {
		t.Fatal("end-before-start accepted")
	}
}

func TestEmptyIntervalNoSamples(t *testing.T) {
	cfg := DefaultConfig(0, 0)
	pcm, stats, err := RenderSamples(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) != 0 || stats.Samples != 0 {
		t.Fatalf("pcm %d samples %d", len(pcm), stats.Samples)
	}
	if !stats.Finite {
		t.Fatal("empty stats not finite")
	}
}
