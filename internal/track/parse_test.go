package track

import (
	"os"
	"path/filepath"
	"testing"

	"mediaregress/internal/fixture"
	"mediaregress/internal/media"
)

func genFixture(t *testing.T, name string) (*fixture.Manifest, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := fixture.Generate(dir)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return m, filepath.Join(dir, name)
}

func TestParseMP4Valid(t *testing.T) {
	m, _ := genFixture(t, "")
	in, err := ParseMP4(m.Valid)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	if len(in.Tracks) != 2 {
		t.Fatalf("tracks=%d want 2: %+v", len(in.Tracks), in.Errors)
	}
	// Tracks are ordered by ID (a0 before v0), not container order.
	var v, a *Track
	for _, tr := range in.Tracks {
		if tr.Kind == KindVideo {
			v = tr
		} else {
			a = tr
		}
	}
	if v == nil || a == nil || v.ID != "v0" || a.ID != "a0" {
		t.Fatalf("ids/kinds: %s %s %s %s", v.ID, v.Kind, a.ID, a.Kind)
	}
	if v.Rotation != 90 {
		t.Errorf("rotation=%d want 90", v.Rotation)
	}
	if v.RotMatrix[0] != 0 || v.RotMatrix[1] != 1<<16 {
		t.Errorf("raw matrix not preserved: %v", v.RotMatrix)
	}
	if v.Crop == nil || v.Crop.Width != 1904 || v.Crop.Height != 1080 || v.Crop.Left != 8 {
		t.Fatalf("crop=%+v", v.Crop)
	}
	if v.Rejected {
		t.Fatalf("valid video rejected: %+v", v.Errors)
	}
	// Two conflicting nclx descriptions must both survive with origins.
	if len(v.Colors) != 2 {
		t.Fatalf("colors=%d want 2", len(v.Colors))
	}
	if v.Colors[0].Source.Offset <= 0 || v.Colors[1].Source.Offset <= v.Colors[0].Source.Offset {
		t.Errorf("color source offsets not ordered: %+v", v.Colors)
	}
	if len(v.Conflicts) == 0 {
		t.Fatal("expected color conflicts")
	}
	fields := map[string]bool{}
	for _, c := range v.Conflicts {
		fields[c.Field] = true
		if len(c.Values) < 2 {
			t.Errorf("conflict %s has %d values", c.Field, len(c.Values))
		}
	}
	for _, f := range []string{"primaries", "transfer", "matrix", "full_range"} {
		if !fields[f] {
			t.Errorf("missing conflict for %s", f)
		}
	}
	if len(v.Samples) != 150 {
		t.Fatalf("video samples=%d want 150", len(v.Samples))
	}
	if len(a.Samples) != 235 {
		t.Fatalf("audio samples=%d want 235", len(a.Samples))
	}
	if a.Samples[1].Start != media.Ticks(21_333_333) {
		t.Errorf("audio sample 1 start=%d", a.Samples[1].Start)
	}
	// Video: each sample is 3000/90000 s = 33.333 ms.
	if v.Samples[1].Start != media.Ticks(33_333_333) {
		t.Errorf("video sample 1 start=%d", v.Samples[1].Start)
	}
	if v.Samples[0].Offset <= 0 || v.Samples[1].Offset != v.Samples[0].Offset+int64(v.Samples[0].Size) {
		t.Errorf("video sample offsets not contiguous: %+v", v.Samples[:2])
	}
	if in.Digest == "" || in.Size == 0 {
		t.Error("input digest/size missing")
	}
}

func TestParseMP4BadCropRejectsTrack(t *testing.T) {
	m, _ := genFixture(t, "")
	in, _ := ParseMP4(m.BadCrop)
	if len(in.Tracks) == 0 {
		t.Fatal("no tracks parsed")
	}
	var v *Track
	for _, tr := range in.Tracks {
		if tr.Kind == KindVideo {
			v = tr
		}
	}
	if v == nil {
		t.Fatal("video track missing")
	}
	if !v.Rejected {
		t.Fatalf("cropped track not rejected: %+v", v.Crop)
	}
	off, first, ok := v.FirstOffset()
	if !ok {
		t.Fatal("no error offset")
	}
	if off != m.BadCropOffset {
		t.Errorf("offset=%d want clap offset %d", off, m.BadCropOffset)
	}
	if first.Class != media.ClassInvalidCrop {
		t.Errorf("class=%s want invalid_crop", first.Class)
	}
	// Rejected means downstream must skip it.
	if got := in.ValidTracks(); len(got) != 1 || got[0].Kind != KindAudio {
		t.Errorf("ValidTracks=%v", got)
	}
}

func TestParseMP4TruncatedReportsFirstMissingByte(t *testing.T) {
	m, _ := genFixture(t, "")
	in, _ := ParseMP4(m.Damaged)
	if len(in.Errors) == 0 {
		t.Fatal("truncated file produced no error")
	}
	off, first, ok := media.FirstOffset(in.Errors)
	if !ok {
		t.Fatal("no positioned error")
	}
	if d := off - m.DamagedOffset; d < -1 || d > 1 {
		t.Fatalf("offset=%d want %d (+/-1); error=%v", off, m.DamagedOffset, first)
	}
}

func TestParseMP4RotationMatrix(t *testing.T) {
	cases := map[int][9]int32{
		90:  {0, 1 << 16, 0, -(1 << 16), 0, 0, 0, 0, 1 << 30},
		180: {-(1 << 16), 0, 0, 0, -(1 << 16), 0, 0, 0, 1 << 30},
		270: {0, -(1 << 16), 0, 1 << 16, 0, 0, 0, 0, 1 << 30},
		0:   {1 << 16, 0, 0, 0, 1 << 16, 0, 0, 0, 1 << 30},
	}
	for deg, m := range cases {
		if got := rotationDegrees(m); got != deg {
			t.Errorf("rotationDegrees=%d want %d", got, deg)
		}
	}
}

func TestParseMXFValid(t *testing.T) {
	m, _ := genFixture(t, "")
	in, err := ParseMXF(m.MXF)
	if err != nil {
		t.Fatalf("ParseMXF: %v (errors %+v)", err, in.Errors)
	}
	if len(in.Tracks) != 2 {
		t.Fatalf("tracks=%d want 2 errs=%+v", len(in.Tracks), in.Errors)
	}
	v, a := in.Tracks[0], in.Tracks[1]
	if v.Kind != KindVideo || a.Kind != KindAudio {
		t.Fatalf("kinds %s %s", v.Kind, a.Kind)
	}
	if v.Timescale != 1 || v.Duration != 60*media.Second/25 {
		t.Errorf("timescale=%d duration=%d", v.Timescale, v.Duration)
	}
	if len(v.Samples) != 60 || len(a.Samples) != 60 {
		t.Errorf("samples v=%d a=%d", len(v.Samples), len(a.Samples))
	}
	if v.Crop == nil || v.Crop.Width != 1904 || v.Crop.Left != 8 {
		t.Fatalf("mxf crop=%+v errors=%+v", v.Crop, v.Errors)
	}
	if v.Rejected {
		t.Fatalf("valid mxf track rejected: %+v", v.Errors)
	}
	if len(v.Colors) != 1 || v.Colors[0].Primaries != 1 {
		t.Fatalf("colors=%+v", v.Colors)
	}
}

func TestParseMXFConflictingColors(t *testing.T) {
	m, _ := genFixture(t, "")
	in, _ := ParseMXF(m.MXFConflict)
	if len(in.Tracks) == 0 {
		t.Fatal("no tracks")
	}
	v := in.Tracks[0]
	if len(v.Colors) != 2 {
		t.Fatalf("colors=%d want 2", len(v.Colors))
	}
	if len(v.Conflicts) == 0 {
		t.Fatal("conflicting MXF descriptors not recorded")
	}
	if v.Conflicts[0].Field != "primaries" {
		t.Errorf("conflict field=%s", v.Conflicts[0].Field)
	}
	// Raw values are untouched.
	if v.Colors[0].Primaries != 1 || v.Colors[1].Primaries != 9 {
		t.Errorf("raw primaries rewritten: %+v", v.Colors)
	}
}

func TestParseMXFTruncated(t *testing.T) {
	m, _ := genFixture(t, "")
	in, _ := ParseMXF(m.MXFDamaged)
	var errs []*media.Error
	for _, tr := range in.Tracks {
		errs = append(errs, tr.Errors...)
	}
	if len(errs) == 0 {
		t.Fatalf("no track error: %+v", in.Errors)
	}
	off, first, ok := media.FirstOffset(errs)
	if !ok {
		t.Fatalf("no positioned error: %+v errs", errs)
	}
	if d := off - m.MXFDamagedOffset; d < -1 || d > 1 {
		t.Fatalf("offset=%d want %d (+/-1) err=%v", off, m.MXFDamagedOffset, first)
	}
	if first.Track == "" {
		t.Error("truncated essence error is not attributed to a track")
	}
	var truncated int
	for _, tr := range in.Tracks {
		if tr.Truncated {
			truncated++
		}
	}
	if truncated != 1 {
		t.Errorf("truncated tracks=%d want 1", truncated)
	}
	if len(in.Tracks) != 2 {
		t.Errorf("tracks still expected for usable metadata, got %d", len(in.Tracks))
	}
}

func TestParseMP4MissingMoov(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.mp4")
	if err := os.WriteFile(p, []byte("not an mp4 file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, err := ParseMP4(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(in.Errors) == 0 {
		t.Error("errors not attached to input")
	}
}
