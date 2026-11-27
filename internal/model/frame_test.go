package model

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestChecksumAndVerify(t *testing.T) {
	data := []byte("frame-payload")
	got := Checksum(data)
	if len(got) != 64 {
		t.Fatalf("checksum length = %d, want 64 hex chars", len(got))
	}
	if !VerifyChecksum(data, got) {
		t.Fatal("VerifyChecksum rejected matching payload")
	}
	if VerifyChecksum(append(data, 'x'), got) {
		t.Fatal("VerifyChecksum accepted tampered payload")
	}
}

func TestCanonicalHashStable(t *testing.T) {
	f1 := Frame{Track: 1, Sequence: 2, ContainerID: "isom/1", SHA256: "abc"}
	f2 := Frame{Track: 1, Sequence: 2, ContainerID: "isom/1", SHA256: "abc"}
	h1, err := CanonicalHash(f1)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := CanonicalHash(f2)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("identical structs produced different hashes")
	}
	f2.Sequence = 3
	h3, _ := CanonicalHash(f2)
	if h3 == h1 {
		t.Fatal("changed field did not change canonical hash")
	}
}

func TestLayout(t *testing.T) {
	if !LayoutMono.Valid() || !LayoutStereo.Valid() || ChannelLayout("quad").Valid() {
		t.Fatal("layout validity mismatch")
	}
	if LayoutMono.Channels() != 1 || LayoutStereo.Channels() != 2 {
		t.Fatal("channel count mismatch")
	}
}

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	ok, err := SafeJoin(root, filepath.Join("a", "b", "frame.bin"))
	if err != nil || ok != filepath.Join(root, "a", "b", "frame.bin") {
		t.Fatalf("nested join = %q, %v", ok, err)
	}
	for _, bad := range []string{
		"../escape.bin",
		"a/../../escape.bin",
		"/etc/passwd",
	} {
		if _, err := SafeJoin(root, bad); !errors.Is(err, ErrPathEscapesWorkspace) {
			t.Fatalf("SafeJoin(%q) err = %v, want ErrPathEscapesWorkspace", bad, err)
		}
	}
}

func TestAtomicWriteLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out", "result.bin")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(target, []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "\x01\x02\x03" {
		t.Fatalf("content = %v, err %v", got, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(target))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == "" && len(e.Name()) > 0 && e.Name()[0] == '.' {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestStructuredError(t *testing.T) {
	base := errors.New("boom")
	se := WrapError(StageManifest, 3, 42, "bad checksum", base)
	if se.Stage != StageManifest || se.Frame != 3 || se.Offset != 42 {
		t.Fatalf("fields wrong: %+v", se)
	}
	if !errors.Is(se, base) {
		t.Fatal("Unwrap did not expose cause")
	}
	got, ok := AsStructured(se)
	if !ok || got != se {
		t.Fatal("AsStructured failed")
	}
	ne := NewError(StagePlanner, 0, -5, "no candidates")
	if ne.Offset != 0 {
		t.Fatalf("negative offset must normalize to 0, got %d", ne.Offset)
	}
}
