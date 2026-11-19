package pipeline

import (
	"testing"

	"mediakey/internal/reason"
	"mediakey/internal/report"
)

func TestValidateManifestCountMismatch(t *testing.T) {
	h := newHarness(t, 4, 2)
	m := cloneManifest(h.pkg.Manifest)
	m.SegmentCount = 9
	out := Validate(m, h.pkg.License, h.pkg.Ciphertexts, ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base, Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID,
	})
	if out.Manifest.Reason != reason.SEGMENT_COUNT_MISMATCH {
		t.Fatalf("manifest reason = %s", out.Manifest.Reason)
	}
}

func TestValidateDuplicateManifestIndex(t *testing.T) {
	h := newHarness(t, 4, 2)
	m := cloneManifest(h.pkg.Manifest)
	m.Segments = append(m.Segments, m.Segments[1])
	out := Validate(m, h.pkg.License, h.pkg.Ciphertexts, ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base, Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID,
	})
	found := false
	for _, s := range out.Segments {
		if s.Reason == reason.SEGMENT_DUPLICATE {
			found = true
		}
	}
	if !found {
		t.Fatal("duplicate manifest index was not reported")
	}
}

func TestValidateOutOfOrderManifest(t *testing.T) {
	h := newHarness(t, 4, 2)
	m := cloneManifest(h.pkg.Manifest)
	m.Segments[0], m.Segments[1] = m.Segments[1], m.Segments[0]
	out := Validate(m, h.pkg.License, h.pkg.Ciphertexts, ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base, Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID,
	})
	if out.Manifest.Reason != reason.SEGMENT_ORDER {
		t.Fatalf("manifest reason = %s", out.Manifest.Reason)
	}
}

func TestValidateNilManifest(t *testing.T) {
	h := newHarness(t, 2, 2)
	out := Validate(nil, h.pkg.License, nil, ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base, Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID,
	})
	if out.Manifest.Status != report.StatusFailed {
		t.Fatal("nil manifest was not reported as a failure")
	}
}
