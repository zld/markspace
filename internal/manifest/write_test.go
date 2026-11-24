package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zld/avpipe/internal/align"
	"github.com/zld/avpipe/internal/clock"
	"github.com/zld/avpipe/internal/metadata"
)

func TestCanonicalHashStableAndWrite(t *testing.T) {
	alignment := &align.Report{
		Timescale: 1000,
		Segments: []align.AlignedSegment{{
			Sequence: 1,
			Start:    clock.NewTimestamp(0, 1000),
			End:      clock.NewTimestamp(2000, 1000),
		}},
	}
	events := []metadata.Event{
		{Track: "subtitle", Start: clock.NewTimestamp(1, 1000), Text: "b"},
		{Track: "subtitle", Start: clock.NewTimestamp(1, 1000), Text: "b"},
		{Track: "subtitle", Start: clock.NewTimestamp(0, 1000), Text: "a"},
	}
	m1 := New(Inputs{Seed: 7, Alignment: alignment, Events: events})
	m2 := New(Inputs{Seed: 7, Alignment: alignment, Events: events})
	b1, err := CanonicalJSON(m1)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := CanonicalJSON(m2)
	if SHA256(b1) != SHA256(b2) {
		t.Fatal("same inputs produced different manifest hashes")
	}
	if m1.SegmentCount != 1 || len(m1.Events) != 2 {
		t.Fatalf("counts segments=%d events=%d", m1.SegmentCount, len(m1.Events))
	}

	path := filepath.Join(t.TempDir(), "nested", "manifest.json")
	sha, data, err := WriteFile(path, m1)
	if err != nil {
		t.Fatal(err)
	}
	if sha != SHA256(b1) || string(data) != string(b1) {
		t.Fatal("write returned inconsistent digest/data")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if SHA256(onDisk) != sha {
		t.Fatal("on-disk manifest hash differs")
	}
}
