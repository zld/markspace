package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"mediacheck/internal/gap"
)

func TestEncodeStableAndWriteAtomic(t *testing.T) {
	m := &Manifest{
		Case: "normal", TrackID: "cam-1", SourceSHA256: "abc",
		SampleRate: 48000, Channels: 2,
		Segments: []Segment{{Index: 0, TrackID: "cam-1", TargetBPS: 2304000}},
		Gaps:     []gap.Gap{},
	}
	d1, b1, err := m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	d2, b2, err := m.Digest()
	if err != nil || d1 != d2 || string(b1) != string(b2) {
		t.Fatal("manifest digest/bytes not stable")
	}
	path := filepath.Join(t.TempDir(), "nested", "manifest.json")
	gotDigest, err := m.WriteFile(path)
	if err != nil || gotDigest != d1 {
		t.Fatalf("write digest=%s err=%v", gotDigest, err)
	}
	read, err := os.ReadFile(path)
	if err != nil || string(read) != string(b1) {
		t.Fatalf("written bytes differ err=%v", err)
	}
}
