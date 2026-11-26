package timeline

import (
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/clock"
)

func TestLoadSwitchRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "switch.jsonl")
	content := `{"source":"primary","seq":100,"timestamp":48000,"clock_rate":48000,"program_ns":1000000000}
{"source":"backup","seq":101,"timestamp":96000,"clock_rate":48000,"program_ns":2000000000}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	recs, err := LoadSwitchRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[1].Source != "backup" || recs[0].Seq != 100 {
		t.Fatalf("records = %+v", recs)
	}

	// Bad clock rate with line location.
	bad := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(bad, []byte(
		`{"source":"x","seq":1,"timestamp":1,"clock_rate":0,"program_ns":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadSwitchRecords(bad)
	me := clock.AsMedia(err)
	if me == nil || me.Line != 1 || me.Kind != clock.KindInvalidInput {
		t.Fatalf("want line-1 structured error, got %+v", me)
	}

	// Empty file is invalid.
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSwitchRecords(empty); clock.KindOf(err) != clock.KindInvalidInput {
		t.Fatalf("want invalid input for empty record file, got %v", err)
	}
}
