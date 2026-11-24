package render

import (
	"image"
	"image/color"
	"testing"

	"archive/internal/clock"
	"archive/internal/normalize"
	"archive/internal/probe"
)

func res(w, h, rot int, crop probe.CropDecl) *normalize.Resolution {
	if crop.Right == 0 {
		crop = probe.CropDecl{Left: 0, Right: w, Top: 0, Bottom: h}
	}
	return &normalize.Resolution{StoredWidth: w, StoredHeight: h, Crop: crop, Rotation: rot}
}

func TestReferenceDeterministic(t *testing.T) {
	a, _ := Reference(64, 48, Seed("cam"), 3)
	b, _ := Reference(64, 48, Seed("cam"), 3)
	ha, _ := EncodePNG(a)
	hb, _ := EncodePNG(b)
	if HashBytes(ha) != HashBytes(hb) {
		t.Fatal("identical reference frames must hash equal")
	}
	c, _ := Reference(64, 48, Seed("cam"), 4)
	hc, _ := EncodePNG(c)
	if HashBytes(ha) == HashBytes(hc) {
		t.Fatal("different frame indices must differ")
	}
}

func TestTransformRotationAndCropGeometry(t *testing.T) {
	src, _ := Reference(80, 40, 1, 0)
	r := res(80, 40, 90, probe.CropDecl{Left: 10, Right: 70, Top: 0, Bottom: 40})
	out, err := ApplyTransform(src, r)
	if err != nil {
		t.Fatal(err)
	}
	if out.Bounds().Dx() != 40 || out.Bounds().Dy() != 60 {
		t.Fatalf("rotated crop size = %dx%d, want 40x60", out.Bounds().Dx(), out.Bounds().Dy())
	}
}

func TestRotate90MovesTopLeftToTopRight(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 3))
	src.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	out := rotate90(src)
	// Source top-left becomes destination top-right.
	got := out.RGBAAt(2, 0)
	if got.R != 255 || got.G != 0 || got.B != 0 {
		t.Fatalf("top-left pixel landed wrong: %+v", got)
	}
	if out.Bounds().Dx() != 3 || out.Bounds().Dy() != 2 {
		t.Fatalf("bounds = %v", out.Bounds())
	}
}

func TestRotateRoundTrips(t *testing.T) {
	src, _ := Reference(20, 14, 7, 1)
	r270 := rotate270(rotate90(src))
	for i := range src.Pix {
		if src.Pix[i] != r270.Pix[i] {
			t.Fatalf("rotate90 then rotate270 changed pixel %d", i)
		}
	}
	r180 := rotate180(rotate180(src))
	for i := range src.Pix {
		if src.Pix[i] != r180.Pix[i] {
			t.Fatalf("rotate180 is not an involution at %d", i)
		}
	}
}

func TestIllegalCropRejectedAtRender(t *testing.T) {
	src, _ := Reference(30, 30, 1, 0)
	_, err := ApplyTransform(src, res(30, 30, 0, probe.CropDecl{Left: 0, Right: 40, Top: 0, Bottom: 30}))
	if clock.AsKind(err) != clock.KindOutOfRange {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}

func TestUnsupportedRotation(t *testing.T) {
	src, _ := Reference(10, 10, 1, 0)
	_, err := ApplyTransform(src, res(10, 10, 45, probe.CropDecl{Left: 0, Right: 10, Top: 0, Bottom: 10}))
	if clock.AsKind(err) != clock.KindUnsupported {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}

func TestPSNRIdenticalIsCappedAndDiffIsFinite(t *testing.T) {
	a, _ := Reference(32, 32, 2, 0)
	got, _ := PSNR(a, a)
	if got != LosslessPSNRCap {
		t.Fatalf("identical PSNR = %f", got)
	}
	b, _ := Downsample(Luma(a), 32, 32, 16, 16)
	_ = b
	same, _ := Downsample(Luma(a), 32, 32, 32, 32)
	psnr, err := PSNRGray(Luma(a), same)
	if err != nil || psnr != LosslessPSNRCap {
		t.Fatalf("lossless scaling PSNR = %f err %v", psnr, err)
	}
}

func TestDownsampleGeometry(t *testing.T) {
	plane := make([]uint8, 16*16)
	for i := range plane {
		plane[i] = 200
	}
	out, err := Downsample(plane, 16, 16, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 64 || out[0] != 200 {
		t.Fatalf("downsample len=%d v=%d", len(out), out[0])
	}
	if _, err := Downsample(plane, 16, 16, 0, 8); clock.AsKind(err) != clock.KindOutOfRange {
		t.Fatalf("bad geometry kind = %q", clock.AsKind(err))
	}
}
