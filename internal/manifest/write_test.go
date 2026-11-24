package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleManifest() *Manifest {
	frames := []FrameEntry{
		{TrackID: "b", TimestampNS: 40000000, FrameIndex: 1, File: "b-0001.png"},
		{TrackID: "a", TimestampNS: 0, FrameIndex: 0, File: "a-0000.png"},
		{TrackID: "a", TimestampNS: 40000000, FrameIndex: 1, File: "a-0001.png"},
	}
	return Build(80000000, 0, frames, nil, nil)
}

func TestBuildSortsFramesDeterministically(t *testing.T) {
	m := sampleManifest()
	got := []string{m.Frames[0].File, m.Frames[1].File, m.Frames[2].File}
	want := []string{"a-0000.png", "a-0001.png", "b-0001.png"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestJSONIsByteStable(t *testing.T) {
	a, _ := sampleManifest().JSON()
	b, _ := sampleManifest().JSON()
	if string(a) != string(b) {
		t.Fatal("manifest JSON not byte-stable")
	}
	if !strings.HasSuffix(string(a), "\n") {
		t.Fatal("manifest should end with newline")
	}
}

func TestWriteFramesRecomputesHashesFromDisk(t *testing.T) {
	dir := t.TempDir()
	m := sampleManifest()
	pngs := map[string][]byte{
		"a-0000.png": []byte("alpha"),
		"a-0001.png": []byte("beta"),
		"b-0001.png": []byte("gamma"),
	}
	if err := m.WriteFrames(dir, pngs); err != nil {
		t.Fatal(err)
	}
	for _, f := range m.Frames {
		d, err := os.ReadFile(filepath.Join(dir, f.File))
		if err != nil {
			t.Fatal(err)
		}
		if f.SHA256 != HashSHA256(d) {
			t.Fatalf("%s hash mismatch", f.File)
		}
	}
	if m.AggregateHash() == "" {
		t.Fatal("aggregate hash empty")
	}
}

func TestWriteFramesRejectsMissingPayload(t *testing.T) {
	m := sampleManifest()
	if err := m.WriteFrames(t.TempDir(), map[string][]byte{}); err == nil {
		t.Fatal("expected error for missing frame payload")
	}
}

func TestAggregateHashChangesWithContent(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	a := sampleManifest()
	b := sampleManifest()
	if err := a.WriteFrames(dirA, map[string][]byte{"a-0000.png": []byte("1"), "a-0001.png": []byte("2"), "b-0001.png": []byte("3")}); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteFrames(dirB, map[string][]byte{"a-0000.png": []byte("1"), "a-0001.png": []byte("2"), "b-0001.png": []byte("X")}); err != nil {
		t.Fatal(err)
	}
	if a.AggregateHash() == b.AggregateHash() {
		t.Fatal("aggregate hash must reflect frame content")
	}
}

func TestFrameFileNameIsSafe(t *testing.T) {
	if got := FrameFileName("cam/../x", 7); strings.ContainsAny(got, "/.") && got != "cam____x-0007.png" {
		t.Fatalf("unsafe name %q", got)
	}
	if got := FrameFileName("cam", 12); got != "cam-0012.png" {
		t.Fatalf("name = %q", got)
	}
}
