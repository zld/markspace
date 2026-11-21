package integrity

import (
	"path/filepath"
	"testing"

	"hlsflow/internal/config"
	"hlsflow/internal/safeio"
)

func testJob(t *testing.T) *config.Job {
	t.Helper()
	return &config.Job{
		Renditions: []config.Rendition{
			{Name: "720p", Bitrate: 1800000, Codec: "h264", FirstSeq: 0, LastSeq: 2},
		},
		Faults: []config.FaultInjection{
			{Rendition: "720p", Segment: 1, Offset: 4, XOR: 0xFF},
		},
	}
}

func TestIndexSaveLoad(t *testing.T) {
	root, err := safeio.EnsureRoot(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	idx := New()
	idx.Add("720p", Entry{Segment: 0, File: "720p/seg-0.ts", SHA256: "abc", Size: 10, Duration: 2})
	idx.Add("720p", Entry{Segment: 0, File: "720p/seg-0.ts", SHA256: "def", Size: 10, Duration: 2})
	if err := idx.Save(root); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	entries := got.Renditions["720p"].Entries
	if len(entries) != 1 || entries[0].SHA256 != "def" {
		t.Fatalf("replace semantics broken: %+v", entries)
	}
}

func TestFaultInjectionIsInMemory(t *testing.T) {
	root, err := safeio.EnsureRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("0123456789abcdef")
	if err := root.AtomicWrite("720p/seg-1.ts", original); err != nil {
		t.Fatal(err)
	}
	job := testJob(t)
	seen, err := OpenSegment(root, job, "720p", "720p/seg-1.ts", 1)
	if err != nil {
		t.Fatal(err)
	}
	if Equal(seen, original) {
		t.Fatal("fault was not applied")
	}
	if seen[4] != original[4]^0xFF {
		t.Fatalf("xor byte = %#x", seen[4])
	}
	// Disk content must be untouched.
	onDisk, err := root.ReadFile("720p/seg-1.ts")
	if err != nil || !Equal(onDisk, original) {
		t.Fatalf("on-disk content was mutated: %v", err)
	}
	// A segment without a fault reads through unchanged.
	plain, err := OpenSegment(root, job, "720p", "720p/seg-1.ts", 2)
	if err != nil || !Equal(plain, original) {
		t.Fatalf("unexpected fault on segment 2: %v", err)
	}
}

func TestHashStable(t *testing.T) {
	if HashBytes([]byte("x")) != HashBytes([]byte("x")) {
		t.Fatal("hash not deterministic")
	}
	if HashBytes([]byte("x")) == HashBytes([]byte("y")) {
		t.Fatal("distinct content hashed equal")
	}
}
