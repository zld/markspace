package manifest_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/manifest"
	"continuity/internal/media"
)

const v1Manifest = `{
  "manifest_version": 1,
  "clips": [
    {"id": 2, "type": "audio", "rate": 44100, "ch": 6, "layout": "5.1"}
  ],
  "quality": {"coverage_min_ppm": 999000, "max_skew_ms": 20}
}`

func TestAdaptV1ThroughStagesWithRollbackPoints(t *testing.T) {
	rollback := t.TempDir()
	m, report, err := manifest.AdaptBytes([]byte(v1Manifest), manifest.AdaptOptions{RollbackDir: rollback})
	if err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != manifest.CanonicalVersion {
		t.Fatalf("version=%s", m.SchemaVersion)
	}
	if len(report.Stages) != 2 || len(report.RollbackPoints) != 2 {
		t.Fatalf("report=%+v", report)
	}
	if len(m.Tracks) != 1 || m.Tracks[0].InputSampleRate != 44100 || m.Tracks[0].Channels != 6 {
		t.Fatalf("track migration bad: %+v", m.Tracks)
	}
	if m.Constraints.MinCoveragePPM != 999000 || m.Constraints.MaxAVSkewMS != 20 {
		t.Fatalf("constraints bad: %+v", m.Constraints)
	}
	for _, p := range report.RollbackPoints {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("rollback point missing: %v", err)
		}
	}

	dest := filepath.Join(t.TempDir(), "restored.json")
	if err := manifest.RestoreRollback(report.RollbackPoints[0], dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), `"manifest_version": 1`) {
		t.Fatalf("rollback did not preserve v1 snapshot:\n%s", data)
	}
}

func TestAdaptUnknownMajorVersion(t *testing.T) {
	_, _, err := manifest.AdaptBytes([]byte(`{"schema_version":"9.2"}`), manifest.AdaptOptions{})
	if !errors.Is(err, media.ErrUnsupportedVersion) {
		t.Fatalf("err=%v", err)
	}
}

func TestAdaptFieldConflictPreservesOriginalInput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "manifest.json")
	original := []byte(`{
      "schema_version":"2.0.0",
      "tracks":[{"id":2,"input_sample_rate_hz":48000,"sample_rate":44100}],
      "constraints":{}
    }`)
	if err := os.WriteFile(input, original, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := manifest.AdaptFile(input, manifest.AdaptOptions{RollbackDir: filepath.Join(dir, "rb")})
	if !errors.Is(err, media.ErrFieldConflict) {
		t.Fatalf("err=%v", err)
	}
	after, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("original manifest was modified")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
