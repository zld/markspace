package pipeline

import (
	"testing"
	"time"

	"mediakey/internal/asset"
	"mediakey/internal/reason"
	generator "mediakey/testdata"
)

// harness builds a packaged asset with deterministic entropy and a fixed
// clock, so every test starts from an identical state.
type harness struct {
	cfg  Config
	pkg  *Packaged
	base time.Time
}

func newHarness(t *testing.T, segments int, rotateEvery int) *harness {
	t.Helper()
	base := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	segs, err := generator.Generate(generator.Options{Seed: 11, SegmentCount: segments, SegmentSize: 96})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		AssetID:        "asset-test",
		Segments:       segs,
		RotateEvery:    rotateEvery,
		LicenseTTL:     time.Minute,
		CorruptSegment: -1,
		DropSegment:    -1,
		Clock:          NewClock(base),
		KeyRandom:      generator.NewDeterministicStream(11, "keys"),
		NonceRandom:    generator.NewDeterministicStream(11, "nonces"),
		SecretRandom:   generator.NewDeterministicStream(11, "secret"),
	}
	pkg, err := Package(cfg)
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	return &harness{cfg: cfg, pkg: pkg, base: base}
}

func (h *harness) validate(t *testing.T, mutate func(*ValidateOptions)) *Outcome {
	t.Helper()
	opts := ValidateOptions{
		Secret:      h.pkg.Secret,
		Now:         h.base,
		Distributor: h.pkg.Distributor,
		AssetID:     h.cfg.AssetID,
	}
	if mutate != nil {
		mutate(&opts)
	}
	return Validate(h.pkg.Manifest, h.pkg.License, h.pkg.Ciphertexts, opts)
}

func assertOnlyFailed(t *testing.T, out *Outcome, index int, code reason.Code) {
	t.Helper()
	if len(out.Segments) != 12 {
		t.Fatalf("%d segment results, want 12", len(out.Segments))
	}
	failed := 0
	for _, s := range out.Segments {
		if s.Reason.IsFailure() {
			failed++
			if s.SegmentIndex != index || s.Reason != code {
				t.Fatalf("unexpected failure at %d: %s", s.SegmentIndex, s.Reason)
			}
		}
	}
	if failed != 1 {
		t.Fatalf("%d failures, want 1", failed)
	}
}

func cloneCiphertexts(in map[int][]byte) map[int][]byte {
	out := make(map[int][]byte, len(in))
	for k, v := range in {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

func cloneManifest(in *asset.Manifest) *asset.Manifest {
	out := *in
	out.Segments = append([]asset.Segment(nil), in.Segments...)
	return &out
}
