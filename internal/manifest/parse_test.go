package manifest

import (
	"encoding/json"
	"strings"
	"testing"

	"mediasync/internal/model"
)

func validJSON(t *testing.T) string {
	t.Helper()
	m := Manifest{
		Version: Version, BaseTimescale: 1_000_000_000,
		Tracks: []Track{{
			ID: 1, Kind: model.KindVideo, Timescale: 90_000,
			StartTicks: 9000, DurationTicks: 180_000,
			MediaFile: "cam1.mp4",
			Edit:      []Edit{{StartTicks: 0, DurationTicks: 180_000, MediaTimeTick: 4500}},
			Keyframes: []int64{0, 90_000},
		}},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func parseString(t *testing.T, s string) (*Manifest, error) {
	t.Helper()
	return Parse(strings.NewReader(s), "manifest.json")
}

func TestParseValid(t *testing.T) {
	m, err := parseString(t, validJSON(t))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(m.Tracks))
	}
	tr := m.Tracks[0]
	if tr.StartTicks != 9000 || tr.Timescale != 90_000 {
		t.Errorf("track fields not preserved: %+v", tr)
	}
	if len(tr.Edit) != 1 || tr.Edit[0].MediaTimeTick != 4500 {
		t.Errorf("edit list not preserved: %+v", tr.Edit)
	}
	if len(tr.Keyframes) != 2 {
		t.Errorf("keyframe index not preserved: %v", tr.Keyframes)
	}
	start, err := tr.Start()
	if err != nil {
		t.Fatal(err)
	}
	if start != model.Nanos(100_000_000) {
		t.Errorf("start = %d, want 100ms", start)
	}
	end, err := tr.End()
	if err != nil {
		t.Fatal(err)
	}
	if end != start+model.Nanos(2_000_000_000) {
		t.Errorf("end = %d", end)
	}
	kf, err := tr.KeyframeTimes()
	if err != nil {
		t.Fatal(err)
	}
	if len(kf) != 2 || kf[1] != model.Nanos(1_000_000_000) {
		t.Errorf("keyframe times = %v", kf)
	}
}

func TestParseMissingFieldNamesTrackAndField(t *testing.T) {
	s := strings.Replace(validJSON(t), `"duration_ticks":180000,`, ``, 1)
	_, err := parseString(t, s)
	if err == nil {
		t.Fatal("expected error for missing duration_ticks")
	}
	msg := err.Error()
	for _, want := range []string{"manifest.missing_field", "manifest.json", "track_index=0", "duration_ticks"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestParseNegativeDuration(t *testing.T) {
	s := strings.Replace(validJSON(t), `"duration_ticks":180000`, `"duration_ticks":-5`, 1)
	_, err := parseString(t, s)
	if err == nil {
		t.Fatal("expected error for negative duration")
	}
	if !strings.Contains(err.Error(), model.CodeManifestNegative) ||
		!strings.Contains(err.Error(), "duration_ticks") {
		t.Errorf("error %q must name the negative field", err)
	}
}

func TestParseNegativeEditDurationNamesPosition(t *testing.T) {
	s := strings.Replace(validJSON(t), `"duration_ticks":180000,"media_time_ticks":4500`, `"duration_ticks":-1,"media_time_ticks":4500`, 1)
	// The replacement above only matches the edit entry, not the track line.
	_, err := parseString(t, s)
	if err == nil {
		t.Fatal("expected error for negative edit duration")
	}
	msg := err.Error()
	if !strings.Contains(msg, "edit[0].duration_ticks") {
		t.Errorf("error %q must name edit position", msg)
	}
}

func TestParseEditOutOfRange(t *testing.T) {
	s := strings.Replace(validJSON(t), `"start_ticks":0,"duration_ticks":180000`, `"start_ticks":0,"duration_ticks":200000`, 1)
	_, err := parseString(t, s)
	if err == nil {
		t.Fatal("expected edit range error")
	}
	if !strings.Contains(err.Error(), model.CodeManifestEditRange) {
		t.Errorf("got %v, want edit range code", err)
	}
}

func TestParseOverlappingEdits(t *testing.T) {
	m := validJSON(t)
	m = strings.Replace(m, `"media_time_ticks":4500}`, `"media_time_ticks":4500},{"start_ticks":1000,"duration_ticks":1000,"media_time_ticks":0}`, 1)
	_, err := parseString(t, m)
	if err == nil {
		t.Fatal("expected overlapping edit error")
	}
	if !strings.Contains(err.Error(), "before previous segment ends") {
		t.Errorf("got %v", err)
	}
}

func TestParseKeyframesMustIncrease(t *testing.T) {
	s := strings.Replace(validJSON(t), `[0,90000]`, `[0,90000,45000]`, 1)
	_, err := parseString(t, s)
	if err == nil {
		t.Fatal("expected keyframe order error")
	}
	if !strings.Contains(err.Error(), "keyframes[2]") {
		t.Errorf("error must name the offending index: %v", err)
	}
}

func TestParseKeyframeOutsideDuration(t *testing.T) {
	s := strings.Replace(validJSON(t), `[0,90000]`, `[0,200000]`, 1)
	_, err := parseString(t, s)
	if err == nil {
		t.Fatal("expected keyframe outside duration")
	}
	if !strings.Contains(err.Error(), "outside track duration") {
		t.Errorf("got %v", err)
	}
}

func TestParseRejectsPathTraversal(t *testing.T) {
	s := strings.Replace(validJSON(t), `"cam1.mp4"`, `"../cam1.mp4"`, 1)
	_, err := parseString(t, s)
	if err == nil {
		t.Fatal("expected path traversal rejection")
	}
	if !strings.Contains(err.Error(), "inside the working directory") {
		t.Errorf("got %v", err)
	}
}

func TestParseDuplicateTrackID(t *testing.T) {
	one := validJSON(t)
	// Duplicate the single track entry by rebuilding the tracks array.
	m, err := parseString(t, one)
	if err != nil {
		t.Fatal(err)
	}
	m.Tracks = append(m.Tracks, m.Tracks[0])
	b, _ := json.Marshal(m)
	m2, perr := parseString(t, string(b))
	if perr == nil {
		t.Fatal("expected duplicate track error")
	}
	if !strings.Contains(perr.Error(), model.CodeManifestDuplicate) {
		t.Errorf("got %v", perr)
	}
	if len(m2.Tracks) != 2 {
		t.Errorf("both tracks should still be reported for diagnosis, got %d", len(m2.Tracks))
	}
}

func TestParseBadJSON(t *testing.T) {
	_, err := parseString(t, `{"version":1,`)
	if err == nil {
		t.Fatal("expected syntax error")
	}
	if !strings.Contains(err.Error(), model.CodeManifestSyntax) {
		t.Errorf("got %v", err)
	}
}

func TestParseReportsAllFindings(t *testing.T) {
	s := strings.Replace(validJSON(t), `"timescale":90000`, `"timescale":0`, 1)
	s = strings.Replace(s, `"duration_ticks":180000,`, ``, 1)
	_, err := parseString(t, s)
	if err == nil {
		t.Fatal("expected findings")
	}
	if n := strings.Count(err.Error(), ";"); n < 1 {
		t.Errorf("expected multiple findings, got %q", err)
	}
}
