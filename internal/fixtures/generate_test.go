package fixtures

import (
	"os"
	"path/filepath"
	"testing"

	"dashforge/internal/input"
)

func TestValidFixtureStructuralValidation(t *testing.T) {
	meta := Valid()
	if len(meta.Renditions) != 3 {
		t.Fatalf("want 3 renditions, got %d", len(meta.Renditions))
	}
	total := 0
	for _, rd := range meta.Renditions {
		total += len(rd.Segments)
		if len(rd.Segments) != segPerRung {
			t.Errorf("rendition %s has %d segments, want %d", rd.ID, len(rd.Segments), segPerRung)
		}
		// Final fragment is intentionally short.
		if last := rd.Segments[len(rd.Segments)-1]; last.DurationMs >= segDurMs {
			t.Errorf("rendition %s final fragment not short: %d", rd.ID, last.DurationMs)
		}
	}
	if total != 36 {
		t.Fatalf("want 36 total segments, got %d", total)
	}
	if err := input.Validate(meta, 32, 10000); err != nil {
		t.Fatalf("valid fixture must pass input validation: %v", err)
	}
}

func TestNegativeFixturesRemainStructurallyParseable(t *testing.T) {
	// Negative fixtures must fail semantic/timeline validation, not JSON
	// parsing — otherwise the CLI cannot report rendition-level diagnostics.
	offset := Offset()
	if got := findRung(offset, "v-720p").StartOffsetMs; got != 33 {
		t.Fatalf("offset fixture shift = %d, want 33", got)
	}
	missing := MissingNumber()
	rd := findRung(missing, "v-360p")
	if len(rd.Segments) != segPerRung-1 {
		t.Fatalf("missing-number fixture should drop one segment, got %d", len(rd.Segments))
	}
	for _, s := range rd.Segments {
		if s.Number == 7 {
			t.Fatal("segment 7 should have been removed")
		}
	}
	drift := DurationDrift()
	if n := len(findRung(drift, "v-1080p").Segments); n != segPerRung+2 {
		t.Fatalf("drift fixture should add two segments, got %d", n)
	}
	// All negative fixtures remain structurally valid inputs.
	for name, meta := range All() {
		if err := input.Validate(meta, 32, 10000); err != nil {
			t.Errorf("fixture %q broke structural validation: %v", name, err)
		}
	}
}

func TestWriteAllAndPlaceholders(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAll(dir, true); err != nil {
		t.Fatal(err)
	}
	for name, file := range Fixtures {
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			t.Errorf("fixture %s not written: %v", name, err)
		}
	}
	// The valid fixture references 36 media files + 3 init segments.
	mediaRoot := filepath.Join(dir, "media")
	count := 0
	err := filepath.Walk(mediaRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 39 {
		t.Fatalf("placeholder file count = %d, want 39", count)
	}
}
