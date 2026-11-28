package ingest_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/fixture"
	"mediapack/internal/ingest"
	"mediapack/internal/model"
)

func demoSpec() fixture.Spec {
	return fixture.Spec{
		Name: "demo",
		Video: fixture.VideoSpec{
			TrackID: 1, Timescale: 24000, Width: 320, Height: 240,
			Rotation: 90,
			Crop: &fixture.CropSpec{
				CleanWidthN: 300, CleanWidthD: 1,
				CleanHeightN: 220, CleanHeightD: 1,
			},
			Colors: []fixture.ColorSpec{
				{Type: "nclx", Primaries: 1, Transfer: 1, Matrix: 1},
				{Type: "nclx", Primaries: 9, Transfer: 16, Matrix: 9, FullRange: true},
			},
			Delta: 1001, Frames: 12,
		},
		Audio: []fixture.AudioSpec{
			{TrackID: 2, Timescale: 48000, Channels: 6, SampleRate: 48000,
				SampleSize: 16, Delta: 1024, Samples: 8},
		},
	}
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseMP4RetainsFields(t *testing.T) {
	spec := demoSpec()
	p := writeTemp(t, "sample.mp4", fixture.BuildMP4(spec))
	c, err := ingest.ParseMP4(p)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	if c.Format != "mp4" || len(c.Tracks) != 2 {
		t.Fatalf("container = %+v", c)
	}
	v := c.VideoTracks()
	if len(v) != 1 || v[0].Rotation != 90 {
		t.Fatalf("rotation not retained: %+v", v)
	}
	if v[0].Crop == nil || v[0].Crop.CleanWidthN != 300 {
		t.Fatalf("crop not retained: %+v", v[0].Crop)
	}
	if !v[0].ColorUnresolved || len(v[0].Colors) != 2 {
		t.Fatalf("color conflict must stay unresolved, got %+v", v[0])
	}
	if v[0].Colors[1].Source.Offset < 0 || v[0].Colors[1].Source.Path == "" {
		t.Fatal("color source provenance missing")
	}
	if len(v[0].Samples) != 12 || v[0].Samples[0].Delta != 1001 {
		t.Fatalf("samples wrong: %d", len(v[0].Samples))
	}
	a := c.AudioTracks()
	if len(a) != 1 || a[0].Channels != 6 || a[0].SampleRate != 48000 {
		t.Fatalf("audio descriptor wrong: %+v", a)
	}
}

func TestParseMXFRetainsFields(t *testing.T) {
	spec := demoSpec()
	p := writeTemp(t, "sample.mxf", fixture.BuildMXF(spec))
	c, err := ingest.ParseMXF(p)
	if err != nil {
		t.Fatalf("ParseMXF: %v", err)
	}
	if c.Format != "mxf" || len(c.Tracks) != 2 {
		t.Fatalf("container = %+v", c)
	}
	v := c.VideoTracks()[0]
	if v.Rotation != 90 || v.Crop == nil || !v.ColorUnresolved {
		t.Fatalf("MXF video fields wrong: %+v", v)
	}
	if len(v.Samples) != 12 {
		t.Fatalf("MXF samples = %d", len(v.Samples))
	}
	a := c.AudioTracks()[0]
	if a.Channels != 6 || a.SampleRate != 48000 || a.SampleSize != 16 {
		t.Fatalf("MXF audio wrong: %+v", a)
	}
	if src := v.Source("timescale"); src.Offset < 0 || src.Path == "" {
		t.Fatal("MXF provenance missing")
	}
}

func TestTruncatedMP4BoxReportsOffsetAndPath(t *testing.T) {
	raw := fixture.BuildMP4(demoSpec())
	// Cut the tail: the last box (mdat) declaration then runs past EOF.
	cut := raw[:len(raw)-6]
	p := writeTemp(t, "trunc.mp4", cut)
	_, err := ingest.ParseMP4(p)
	fe, ok := model.AsField(err)
	if !ok {
		t.Fatalf("expected FieldError, got %v", err)
	}
	if fe.Kind != model.KindTruncated && fe.Kind != model.KindOutOfRange {
		t.Fatalf("kind = %s", fe.Kind)
	}
	if fe.Offset < 0 || fe.Path == "" {
		t.Fatalf("error lacks offset/path: %+v", fe)
	}
}

func TestOversizedBoxReportsOffset(t *testing.T) {
	raw := fixture.BuildMP4(demoSpec())
	// Inflate the declared size of the first top-level box beyond the file.
	tampered := append([]byte(nil), raw...)
	tampered[0] = 0x7F
	tampered[1] = 0xFF
	p := writeTemp(t, "oversize.mp4", tampered)
	_, err := ingest.ParseMP4(p)
	fe, ok := model.AsField(err)
	if !ok || fe.Kind != model.KindOutOfRange {
		t.Fatalf("expected out_of_range FieldError, got %v", err)
	}
	if fe.Offset != 0 {
		t.Fatalf("offset = %d, want 0", fe.Offset)
	}
}

func TestTruncatedMXFKLVReportsOffsetAndPath(t *testing.T) {
	raw := fixture.BuildMXF(demoSpec())
	cut := raw[:len(raw)-4]
	p := writeTemp(t, "trunc.mxf", cut)
	_, err := ingest.ParseMXF(p)
	fe, ok := model.AsField(err)
	if !ok {
		t.Fatalf("expected FieldError, got %v", err)
	}
	if fe.Kind != model.KindTruncated && fe.Kind != model.KindOutOfRange {
		t.Fatalf("kind = %s", fe.Kind)
	}
	if fe.Offset < 0 || fe.Path == "" {
		t.Fatalf("error lacks offset/path: %+v", fe)
	}
}

func TestBadMXFKLVLength(t *testing.T) {
	raw := fixture.BuildMXF(demoSpec())
	tampered := append([]byte(nil), raw...)
	// The first KLV item: 16-byte key then BER length byte. Force a long
	// form length declaring far more bytes than the file contains.
	tampered[16] = 0x82
	tampered[17] = 0xFF
	tampered[18] = 0xFF
	p := writeTemp(t, "badlen.mxf", tampered)
	_, err := ingest.ParseMXF(p)
	fe, ok := model.AsField(err)
	if !ok || fe.Kind != model.KindOutOfRange {
		t.Fatalf("expected out_of_range FieldError, got %v", err)
	}
	if fe.Offset != 0 || fe.Path == "" {
		t.Fatalf("offset/path wrong: %+v", fe)
	}
}

func TestIllegalCropFallsBackToFullAperture(t *testing.T) {
	spec := demoSpec()
	spec.Video.Crop = &fixture.CropSpec{
		CleanWidthN: 9999, CleanWidthD: 1,
		CleanHeightN: 9999, CleanHeightD: 1,
	}
	p := writeTemp(t, "badcrop.mp4", fixture.BuildMP4(spec))
	c, err := ingest.ParseMP4(p)
	if err != nil {
		t.Fatalf("legal crop fallback must not be fatal: %v", err)
	}
	if c.VideoTracks()[0].Crop != nil {
		t.Fatal("illegal crop must be discarded in favor of full aperture")
	}
	if len(c.Warnings) == 0 {
		t.Fatal("fallback must record a structured warning")
	}
}

func TestDimensionLimitEnforced(t *testing.T) {
	spec := demoSpec()
	spec.Video.Width = 4097
	p := writeTemp(t, "wide.mp4", fixture.BuildMP4(spec))
	_, err := ingest.ParseMP4(p)
	fe, ok := model.AsField(err)
	if !ok || fe.Kind != model.KindOutOfRange {
		t.Fatalf("expected dimension out_of_range, got %v", err)
	}
}
