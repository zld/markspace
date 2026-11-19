package pipeline

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"mediakey/internal/asset"
	"mediakey/internal/reason"
	"mediakey/internal/report"
	generator "mediakey/testdata"
)

func TestPackageRotatesOnSegmentBoundaries(t *testing.T) {
	h := newHarness(t, 12, 3)
	if h.pkg.Manifest.RotationCount != 3 {
		t.Fatalf("rotation count = %d, want 3", h.pkg.Manifest.RotationCount)
	}
	if len(h.pkg.Rotations) != 3 {
		t.Fatalf("%d rotations recorded, want 3", len(h.pkg.Rotations))
	}
	wantIDs := []string{"k1", "k1", "k1", "k2", "k2", "k2", "k3", "k3", "k3", "k4", "k4", "k4"}
	for i, want := range wantIDs {
		seg, ok := h.pkg.Manifest.Segment(i)
		if !ok {
			t.Fatalf("segment %d missing from manifest", i)
		}
		if seg.KeyID != want {
			t.Fatalf("segment %d key = %s, want %s", i, seg.KeyID, want)
		}
	}
	for i, rot := range h.pkg.Rotations {
		if rot.Rotation != i+1 || rot.AtSegment != (i+1)*3 {
			t.Fatalf("rotation %d = %+v", i, rot)
		}
	}
	if h.pkg.License.KeyMin != 1 || h.pkg.License.KeyMax != 4 {
		t.Fatalf("license key range = [%d,%d]", h.pkg.License.KeyMin, h.pkg.License.KeyMax)
	}
}

func TestPackageNeverReusesNonces(t *testing.T) {
	h := newHarness(t, 40, 5)
	seen := map[string]int{}
	for _, seg := range h.pkg.Manifest.Segments {
		if owner, dup := seen[seg.Nonce]; dup {
			t.Fatalf("nonce reused by segments %d and %d", owner, seg.Index)
		}
		seen[seg.Nonce] = seg.Index
	}
}

// repeatedNonceSource yields a constant nonce, which forces a collision.
type repeatedNonceSource struct{ nonce []byte }

func (r repeatedNonceSource) Read(p []byte) (int, error) { return copy(p, r.nonce), nil }

func TestPackageAbortsOnDuplicateNonce(t *testing.T) {
	segs, err := generator.Generate(generator.Options{Seed: 2, SegmentCount: 3, SegmentSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		AssetID:        "asset-dup",
		Segments:       segs,
		RotateEvery:    2,
		LicenseTTL:     time.Minute,
		Clock:          NewClock(time.Unix(0, 0)),
		KeyRandom:      generator.NewDeterministicStream(2, "keys"),
		NonceRandom:    repeatedNonceSource{nonce: bytes.Repeat([]byte{9}, asset.NonceSize)},
		SecretRandom:   generator.NewDeterministicStream(2, "secret"),
		CorruptSegment: -1,
		DropSegment:    -1,
	}
	_, err = Package(cfg)
	var pkgErr *PackageError
	if !errors.As(err, &pkgErr) || pkgErr.Code != reason.DUPLICATE_NONCE {
		t.Fatalf("got %v, want DUPLICATE_NONCE", err)
	}
}

func TestPackageRejectsInvalidConfig(t *testing.T) {
	segs, err := generator.Generate(generator.Options{Seed: 1, SegmentCount: 1, SegmentSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	base := Config{AssetID: "a", Segments: segs, RotateEvery: 1, LicenseTTL: time.Minute, Clock: NewClock(time.Unix(0, 0)), CorruptSegment: -1, DropSegment: -1}
	for name, mutate := range map[string]func(*Config){
		"no asset":     func(c *Config) { c.AssetID = "" },
		"no clock":     func(c *Config) { c.Clock = nil },
		"rotate zero":  func(c *Config) { c.RotateEvery = 0 },
		"ttl zero":     func(c *Config) { c.LicenseTTL = 0 },
		"no segments":  func(c *Config) { c.Segments = nil },
		"negative adv": func(c *Config) { c.AdvanceClock = -time.Second },
	} {
		c := base
		mutate(&c)
		var pkgErr *PackageError
		if err := validateConfig(c); !errors.As(err, &pkgErr) || pkgErr.Code != reason.INVALID_INPUT {
			t.Fatalf("%s: got %v, want INVALID_INPUT", name, err)
		}
	}
}

func TestValidateCleanAsset(t *testing.T) {
	h := newHarness(t, 12, 3)
	out := h.validate(t, nil)
	if out.License.Status != report.StatusOK || out.License.Reason != reason.NONE {
		t.Fatalf("license = %+v", out.License)
	}
	if out.Manifest.Status != report.StatusOK {
		t.Fatalf("manifest = %+v", out.Manifest)
	}
	if len(out.Segments) != 12 {
		t.Fatalf("%d segment results, want 12", len(out.Segments))
	}
	for _, s := range out.Segments {
		if s.Status != report.StatusOK || s.Reason != reason.NONE {
			t.Fatalf("segment %d = %+v", s.SegmentIndex, s)
		}
	}
	if out.Failed() {
		t.Fatal("clean asset reported a failure")
	}
}

func TestValidateCorruptSegmentIsAuthFailedOnly(t *testing.T) {
	h := newHarness(t, 12, 3)
	delivered := cloneCiphertexts(h.pkg.Ciphertexts)
	ct := delivered[5]
	ct[len(ct)/2] ^= 0xff
	delivered[5] = ct

	out := Validate(h.pkg.Manifest, h.pkg.License, delivered, ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base, Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID,
	})
	assertOnlyFailed(t, out, 5, reason.AUTH_FAILED)
	if out.Segments[6].Status != report.StatusOK {
		t.Fatal("a later segment was not validated after segment 5 failed")
	}
}

func TestValidateMissingSegmentIsIsolated(t *testing.T) {
	h := newHarness(t, 12, 3)
	delivered := cloneCiphertexts(h.pkg.Ciphertexts)
	delete(delivered, 8)

	out := Validate(h.pkg.Manifest, h.pkg.License, delivered, ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base, Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID,
	})
	assertOnlyFailed(t, out, 8, reason.SEGMENT_MISSING)
	if len(out.Segments) != 12 {
		t.Fatalf("%d segment results, want 12", len(out.Segments))
	}
}
