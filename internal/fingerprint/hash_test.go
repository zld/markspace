package fingerprint

import (
	"testing"

	"avbatch/internal/mediaerr"
)

func sampleSet(seed byte, n int, size int, dur int64) []Sample {
	out := make([]Sample, n)
	for i := range out {
		var d [32]byte
		d[0] = seed
		d[1] = byte(i)
		out[i] = Sample{Digest: d, Size: size + i, Duration: dur, Keyframe: i == 0}
	}
	return out
}

func TestComputeStableAcrossRuns(t *testing.T) {
	cfg := DefaultConfig()
	s := sampleSet(1, 8, 100, 40)
	a, err := cfg.Compute(s)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := cfg.Compute(s)
	if a != b {
		t.Fatal("fingerprint is not deterministic")
	}
	if a.SampleCount != 8 || a.Keyframes != 1 || a.DurationNanos != 320 {
		t.Fatalf("fingerprint = %+v", a)
	}
}

func TestComputeDetectsPayloadChange(t *testing.T) {
	cfg := DefaultConfig()
	a, _ := cfg.Compute(sampleSet(1, 4, 100, 40))
	b, _ := cfg.Compute(sampleSet(2, 4, 100, 40))
	if a.Content == b.Content {
		t.Fatal("different payloads produced the same content digest")
	}
}

func TestDuplicateReportedWithSource(t *testing.T) {
	cfg := DefaultConfig()
	idx, err := NewIndex(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := cfg.Compute(sampleSet(7, 4, 100, 40))
	other, _ := cfg.Compute(sampleSet(9, 4, 100, 40))
	rep := idx.Run([]Fingerprint{fp, other, fp}, []string{"frag0", "frag1", "frag2"})
	if rep.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", rep.Duplicates)
	}
	if rep.Accepted != 2 {
		t.Fatalf("accepted = %d, want 2", rep.Accepted)
	}
	d := rep.Decisions[2]
	if !d.Duplicate || d.SourceSegment != 0 || d.Source != "frag0" {
		t.Fatalf("decision = %+v", d)
	}
	if idx.Len() != 2 {
		t.Fatalf("index length = %d", idx.Len())
	}
}

func TestNearCollisionKeepsContent(t *testing.T) {
	// Force collisions by using a one-byte key. Two different payloads that
	// share the short key must both be kept and the collision reported.
	cfg := Config{ShortBytes: 1, DurationToleranceNanos: 0, MaxSamplesPerSegment: 100}
	idx, _ := NewIndex(cfg)
	a := makeFingerprintWithKey(t, cfg, 0xAB, 100)
	b := makeFingerprintWithKey(t, cfg, 0xAB, 200)
	if a.Short != b.Short {
		t.Skip("constructed keys did not collide")
	}
	rep := idx.Run([]Fingerprint{a, b}, []string{"x", "y"})
	if rep.Duplicates != 0 {
		t.Fatalf("collision was treated as duplicate: %+v", rep)
	}
	if rep.Collisions != 1 || rep.Accepted != 2 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Decisions[1].Duplicate {
		t.Fatal("content was dropped on a failed recheck")
	}
	if idx.Len() != 2 {
		t.Fatalf("collision lost content: index length = %d", idx.Len())
	}
}

// makeFingerprintWithKey builds a fingerprint whose short key is forced to key
// by rewriting the first digest byte that feeds it. It re-derives content so
// the verification step sees a consistent fingerprint.
func makeFingerprintWithKey(t *testing.T, cfg Config, key byte, seed byte) Fingerprint {
	t.Helper()
	samples := sampleSet(seed, 3, 90, 33)
	// Tune a 24-bit counter spread over the payload digests until the short
	// key matches, which proves the production key derivation is exercised
	// rather than stubbed. Two different seeds then share a lookup key while
	// differing everywhere else: exactly the near-collision case.
	for i := 0; i < 1<<20; i++ {
		samples[0].Digest[0] = byte(i)
		samples[0].Digest[1] = byte(i >> 8)
		samples[1].Digest[2] = byte(i >> 16)
		fp, err := cfg.Compute(samples)
		if err != nil {
			t.Fatal(err)
		}
		if byte(fp.Short) == key {
			return fp
		}
	}
	t.Fatalf("could not construct a fingerprint with short key %#x", key)
	return Fingerprint{}
}

func TestDurationMismatchNotDuplicate(t *testing.T) {
	cfg := DefaultConfig()
	idx, _ := NewIndex(cfg)
	a, _ := cfg.Compute(sampleSet(3, 4, 100, 40))
	// Same payload bytes but different durations: not the same content.
	longer := sampleSet(3, 4, 100, 41)
	b, _ := cfg.Compute(longer)
	rep := idx.Run([]Fingerprint{a, b}, []string{"a", "b"})
	if rep.Duplicates != 0 {
		t.Fatalf("duration mismatch treated as duplicate: %+v", rep.Decisions)
	}
}

func TestSampleCountMismatchNotDuplicate(t *testing.T) {
	cfg := DefaultConfig()
	idx, _ := NewIndex(cfg)
	a, _ := cfg.Compute(sampleSet(4, 4, 100, 40))
	b, _ := cfg.Compute(sampleSet(4, 5, 100, 40))
	rep := idx.Run([]Fingerprint{a, b}, []string{"a", "b"})
	if rep.Duplicates != 0 || rep.Accepted != 2 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestDuplicateWithTolerance(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DurationToleranceNanos = 1000
	idx, _ := NewIndex(cfg)
	a, _ := cfg.Compute(sampleSet(5, 4, 100, 40))
	b, _ := cfg.Compute(sampleSet(5, 4, 100, 40))
	// Tolerate small duration jitter by nudging the stored duration only.
	b.DurationNanos += 500
	rep := idx.Run([]Fingerprint{a, b}, []string{"a", "b"})
	if rep.Duplicates != 1 {
		t.Fatalf("duplicates = %d, decisions = %+v", rep.Duplicates, rep.Decisions)
	}
}

func TestConfigValidation(t *testing.T) {
	for _, cfg := range []Config{
		{ShortBytes: 0, MaxSamplesPerSegment: 1},
		{ShortBytes: 33, MaxSamplesPerSegment: 1},
		{ShortBytes: 4, DurationToleranceNanos: -1, MaxSamplesPerSegment: 1},
		{ShortBytes: 4},
	} {
		if _, err := cfg.Compute(sampleSet(1, 1, 1, 1)); err == nil {
			t.Fatalf("config %+v accepted", cfg)
		}
	}
}

func TestEmptySamplesRejected(t *testing.T) {
	if _, err := DefaultConfig().Compute(nil); err == nil {
		t.Fatal("empty sample list accepted")
	}
}

func TestSampleLimit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxSamplesPerSegment = 2
	_, err := cfg.Compute(sampleSet(1, 3, 10, 10))
	if !mediaerr.Is(err, mediaerr.KindInvalidBox) {
		t.Fatalf("err = %v, want invalid_box", err)
	}
}
