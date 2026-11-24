package render

import (
	"testing"

	"archive/internal/clock"
	"archive/internal/normalize"
	"archive/internal/probe"
)

func TestEncodePNGNilImage(t *testing.T) {
	if _, err := EncodePNG(nil); clock.AsKind(err) != clock.KindInvalidMetadata {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}

func TestPSNRSizeMismatchRejected(t *testing.T) {
	a, _ := Reference(10, 10, 1, 0)
	b, _ := Reference(11, 10, 1, 0)
	if _, err := PSNR(a, b); clock.AsKind(err) != clock.KindMisaligned {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}

func TestApplyTransformNilSource(t *testing.T) {
	r := &normalize.Resolution{Crop: probe.CropDecl{Right: 4, Bottom: 4}}
	if _, err := ApplyTransform(nil, r); clock.AsKind(err) != clock.KindInvalidMetadata {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}

func TestReferenceBadSize(t *testing.T) {
	if _, err := Reference(0, 10, 1, 0); clock.AsKind(err) != clock.KindOutOfRange {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}
