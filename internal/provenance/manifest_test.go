package provenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashFileIsStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "in.bin")
	if err := os.WriteFile(path, []byte("hello tsforge"), 0o644); err != nil {
		t.Fatal(err)
	}
	h1, n1, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h2, n2, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 || n1 != n2 {
		t.Fatalf("hash unstable: %s/%d vs %s/%d", h1, n1, h2, n2)
	}
	if n1 != 13 {
		t.Fatalf("size=%d", n1)
	}
}

func TestManifestSelfHashDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in")
	if err := os.WriteFile(input, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := New(input, Params{Seed: 1, SegmentTicks: 90000})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AddSegment(Segment{Number: 0, File: "s.mp4", SHA256: "deadbeef", Size: 3}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
	// Reading a freshly written manifest must validate.
	if _, err := Read(path); err != nil {
		t.Fatalf("fresh manifest rejected: %v", err)
	}
	// Tamper with the segment hash; the self-hash must catch it.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(b), `"sha256": "deadbeef"`, `"sha256": "cafebabe"`, 1)
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("tampered manifest accepted")
	}
}

func TestManifestWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := WriteAtomic(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "second" {
		t.Fatalf("atomic write left %q", b)
	}
	// No temporary files may remain after a successful commit.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestParamsHashIsStable(t *testing.T) {
	p := Params{Seed: 17, SegmentTicks: 90000, IndexLimit: 1000}
	if p.Hash() != p.Hash() {
		t.Fatal("params hash unstable")
	}
	q := p
	q.Seed = 18
	if p.Hash() == q.Hash() {
		t.Fatal("params change did not alter hash")
	}
}

func TestVerifySegmentDetectsSizeAndHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seg.mp4")
	content := []byte("payload")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum, size, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	good, actual, err := VerifySegment(dir, Segment{Number: 0, File: "seg.mp4", Size: size, SHA256: sum})
	if err != nil || !good || actual != sum {
		t.Fatalf("verify good segment: ok=%v actual=%s err=%v", good, actual, err)
	}
	// Hash mismatch.
	good, _, err = VerifySegment(dir, Segment{Number: 0, File: "seg.mp4", Size: size, SHA256: "00"})
	if err != nil || good {
		t.Fatalf("hash mismatch not reported: ok=%v err=%v", good, err)
	}
	// Size mismatch.
	good, _, err = VerifySegment(dir, Segment{Number: 0, File: "seg.mp4", Size: size + 1, SHA256: sum})
	if err == nil || good {
		t.Fatalf("size mismatch not reported: ok=%v err=%v", good, err)
	}
}

func TestManifestSegmentLimit(t *testing.T) {
	m := &Manifest{}
	for i := 0; i < MaxSegments; i++ {
		if err := m.AddSegment(Segment{Number: i}); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if err := m.AddSegment(Segment{Number: MaxSegments}); err == nil {
		t.Fatal("segment limit not enforced")
	}
}
