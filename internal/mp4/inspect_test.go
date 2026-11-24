package mp4

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediasync/internal/fixture"
	"mediasync/internal/model"
)

func writeFixture(t *testing.T, spec fixture.MP4Spec) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.mp4")
	if err := fixture.WriteFile(path, spec); err != nil {
		t.Fatal(err)
	}
	return path
}

func standardSpec() fixture.MP4Spec {
	return fixture.MP4Spec{
		Timescale: 1000,
		Tracks: []fixture.MP4Track{
			{
				ID: 1, Handler: [4]byte{'v', 'i', 'd', 'e'}, Timescale: 90_000,
				SampleDelta: 3000, SampleCount: 30, SyncSamples: []uint32{1, 11, 21},
				Edits: []fixture.MP4Edit{{SegmentDuration: 1000, MediaTime: 9000}},
				Width: 1280 << 16, Height: 720 << 16,
			},
			{
				ID: 2, Handler: [4]byte{'s', 'o', 'u', 'n'}, Timescale: 48_000,
				SampleDelta: 1024, SampleCount: 40,
			},
		},
	}
}

func TestInspectExtractsTracks(t *testing.T) {
	path := writeFixture(t, standardSpec())
	info, err := InspectFile(path)
	if err != nil {
		t.Fatalf("InspectFile: %v", err)
	}
	if info.Timescale != 1000 {
		t.Errorf("movie timescale = %d", info.Timescale)
	}
	if len(info.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(info.Tracks))
	}
	v, a := info.Tracks[0], info.Tracks[1]
	if v.ID != 1 || v.Handler != "vide" || v.Timescale != 90_000 {
		t.Errorf("video track = %+v", v)
	}
	if len(v.Keyframes) != 3 || v.Keyframes[1] != 11 {
		t.Errorf("sparse keyframes not preserved: %v", v.Keyframes)
	}
	if len(v.KeyframeTimes) != 3 {
		t.Fatalf("keyframe times = %v", v.KeyframeTimes)
	}
	// Sample 11 is at 10*3000 = 30000 ticks, plus the edit media time 9000.
	wantFirstKf := int64(9000) * int64(1_000_000_000) / 90_000
	wantSecondKf := int64(9000+10*3000) * int64(1_000_000_000) / 90_000
	if int64(v.KeyframeTimes[0]) != wantFirstKf {
		t.Errorf("first keyframe = %d, want %d", v.KeyframeTimes[0], wantFirstKf)
	}
	if int64(v.KeyframeTimes[1]) != wantSecondKf {
		t.Errorf("second keyframe = %d, want %d", v.KeyframeTimes[1], wantSecondKf)
	}
	if len(v.Edit) != 1 || v.Edit[0].Start != model.Nanos(int64(9000)*model.NanosPerSecond/90_000) {
		t.Errorf("edit list = %+v", v.Edit)
	}
	if len(a.Edit) != 0 {
		t.Errorf("track without edts should have no edits: %+v", a.Edit)
	}
}

func TestInspectTruncatedLocatesDamage(t *testing.T) {
	dir := t.TempDir()
	clean := filepath.Join(dir, "clean.mp4")
	if err := fixture.WriteFile(clean, standardSpec()); err != nil {
		t.Fatal(err)
	}
	// Damage the top-level box length at the mdat boundary.
	data, err := os.ReadFile(clean)
	if err != nil {
		t.Fatal(err)
	}
	damaged := filepath.Join(dir, "damaged.mp4")
	// Cut the file in half and corrupt the first following box header.
	out := append([]byte(nil), data[:len(data)/2]...)
	if err := os.WriteFile(damaged, out, 0o644); err != nil {
		t.Fatal(err)
	}
	info, ierr := InspectFile(damaged)
	if ierr == nil {
		t.Fatal("expected a truncation error")
	}
	if info == nil || len(info.Diagnostics) == 0 {
		t.Fatalf("no diagnostics: %v", ierr)
	}
	d := info.Diagnostics[0]
	if !strings.Contains(d.Code, "mp4.") {
		t.Errorf("code = %s", d.Code)
	}
	if !d.Loc.HasByteOffset {
		t.Error("diagnostic must include a byte offset")
	}
}

func TestInspectDetectsEditBeyondDuration(t *testing.T) {
	spec := standardSpec()
	// media_time is far beyond the media duration.
	spec.Tracks[0].Edits = []fixture.MP4Edit{{SegmentDuration: 2000, MediaTime: 5_000_000}}
	path := writeFixture(t, spec)
	info, err := InspectFile(path)
	if err == nil {
		t.Fatal("expected an edit-range error")
	}
	if info == nil || len(info.Diagnostics) == 0 {
		t.Fatal("expected diagnostics")
	}
	d := info.Diagnostics[0]
	if !strings.Contains(d.Code, "edit_out_of_range") {
		t.Errorf("code = %s, want edit_out_of_range", d.Code)
	}
	if d.Loc.TrackID != 1 {
		t.Errorf("diagnostic track = %d, want 1", d.Loc.TrackID)
	}
	if !strings.Contains(d.Loc.Field, "media_time") {
		t.Errorf("field = %q, want media_time", d.Loc.Field)
	}
}

func TestInspectEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.mp4")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := InspectFile(path)
	if err == nil {
		t.Fatal("expected an error for an empty file")
	}
	if info == nil || len(info.Diagnostics) == 0 {
		t.Fatalf("expected diagnostics, got info=%+v err=%v", info, err)
	}
}

func TestInspectMissingFile(t *testing.T) {
	_, err := InspectFile(filepath.Join(t.TempDir(), "absent.mp4"))
	if err == nil {
		t.Fatal("expected an error")
	}
}
