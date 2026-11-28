package scan

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"avpipe/internal/fixture"
	"avpipe/internal/model"
)

func prepareCase(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	desc := fixture.Cases()[name]
	if err := fixture.WriteCase(dir, desc); err != nil {
		t.Fatalf("write case: %v", err)
	}
	return dir
}

func TestHashStableAndSensitive(t *testing.T) {
	if HashBytes([]byte("abc")) != HashBytes([]byte("abc")) {
		t.Fatal("same bytes must hash equal")
	}
	if HashBytes([]byte("abc")) == HashBytes([]byte("abd")) {
		t.Fatal("different bytes must hash differently")
	}
}

func TestScanNormalCase(t *testing.T) {
	dir := prepareCase(t, "normal")
	idx, err := Scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !idx.Complete || len(idx.Tracks) != 3 {
		t.Fatalf("complete=%v tracks=%d", idx.Complete, len(idx.Tracks))
	}
	if idx.Digest == "" {
		t.Fatal("missing case digest")
	}
	for _, ti := range idx.Tracks {
		if len(ti.Samples) != 8 {
			t.Errorf("track %d samples=%d want 8", ti.Track, len(ti.Samples))
		}
		var prev int64 = -1
		for _, s := range ti.Samples {
			if s.InputOffset <= prev {
				t.Errorf("track %d offsets not strictly increasing", ti.Track)
			}
			prev = s.InputOffset
			if s.Clock.ClockRate == 0 {
				t.Errorf("track %d missing source clock", ti.Track)
			}
		}
	}
	if idx.Mvhd.Timescale == 0 {
		t.Fatal("mvhd timescale not parsed")
	}
}

func TestScanDamagedTruncationRecoverable(t *testing.T) {
	dir := prepareCase(t, "damaged")
	idx, err := Scan(dir)
	if err != nil {
		t.Fatalf("scan must not fail whole case: %v", err)
	}
	var found bool
	for _, ti := range idx.Tracks {
		if ti.Track == 3 {
			found = true
			if !ti.Truncated || ti.Defect == nil || !ti.Defect.Recoverable {
				t.Fatalf("track 3 truncation not reported as recoverable: %+v", ti.Defect)
			}
			if ti.Defect.Code != model.CodeTruncated || ti.Defect.Offset != ti.TruncOffset {
				t.Fatalf("unexpected defect: %+v", ti.Defect)
			}
		} else if ti.Defect != nil && !ti.Defect.Recoverable {
			t.Fatalf("track %d should not carry a fatal defect at scan", ti.Track)
		}
	}
	if !found {
		t.Fatal("track 3 missing")
	}
}

func TestRejectsOutOfRangeDeclaredLength(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, model.RTPPath(1))
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, 9_999_999) // claims far more bytes than exist
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	ti := scanTrack(dir, model.TrackDesc{ID: 1, ClockRate: 90000})
	if ti.Defect == nil || !ti.Defect.Recoverable || ti.Defect.Offset != 0 {
		t.Fatalf("expected recoverable out-of-range defect, got %+v", ti.Defect)
	}
}

func TestIndexReuseAndDigestGuard(t *testing.T) {
	dir := prepareCase(t, "normal")
	idx, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), IndexFile)
	if err := idx.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, idx.Digest); err != nil {
		t.Fatalf("reload with matching digest: %v", err)
	}
	if _, err := Load(path, "deadbeef"); err == nil {
		t.Fatal("digest mismatch must be rejected")
	} else if me, ok := model.AsError(err); !ok || me.Code != model.CodeDigestMismatch {
		t.Fatalf("want digest_mismatch, got %v", err)
	}

	// Simulate an interrupted write that never set the completion marker.
	partial := *idx
	partial.Complete = false
	ppath := filepath.Join(t.TempDir(), IndexFile)
	data, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ppath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(ppath, idx.Digest); err == nil {
		t.Fatal("incomplete index must not be reused")
	} else if me, ok := model.AsError(err); !ok || me.Code != model.CodeIndexIncomplete {
		t.Fatalf("want index_incomplete, got %v", err)
	}
}
