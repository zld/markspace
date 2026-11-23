package index

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/probe"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func box(typ string, payload []byte, children ...[]byte) []byte {
	body := append([]byte{}, payload...)
	for _, c := range children {
		body = append(body, c...)
	}
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(body)))
	copy(hdr[4:], []byte(typ))
	return append(hdr, body...)
}

func sampleMP4() []byte {
	tk := make([]byte, 96)
	binary.BigEndian.PutUint32(tk[12:], 1)
	binary.BigEndian.PutUint32(tk[40:], 1<<16)
	binary.BigEndian.PutUint32(tk[56:], 1<<16)
	md := make([]byte, 32)
	binary.BigEndian.PutUint32(md[12:], 90000)
	binary.BigEndian.PutUint32(md[16:], 45000)
	hd := make([]byte, 24)
	copy(hd[8:], []byte("vide"))
	sz := make([]byte, 12)
	binary.BigEndian.PutUint32(sz[8:], 50)
	trak := box("trak", nil,
		box("tkhd", tk), box("mdhd", md), box("hdlr", hd), box("stsz", sz))
	return append(box("ftyp", []byte("isom\x00\x00\x00\x00")), box("moov", nil, trak)...)
}

func writeInput(t *testing.T, data []byte) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "in.mp4")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func TestGetOrScanCaches(t *testing.T) {
	dir, path := writeInput(t, sampleMP4())
	st, err := NewStore(filepath.Join(dir, "idx"))
	if err != nil {
		t.Fatal(err)
	}
	e1, err := st.GetOrScan(path, probe.DefaultLimits())
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	e2, err := st.GetOrScan(path, probe.DefaultLimits())
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if e1.Summary.SHA256 != e2.Summary.SHA256 || e2.Meta == nil ||
		len(e2.Tracks) != 1 || e2.Tracks[0].Samples != 50 {
		t.Fatalf("cached entry mismatch: %+v", e2)
	}
	if files := countFiles(t, st.dir); files != 1 {
		t.Fatalf("want exactly 1 index file, got %d", files)
	}
}

func TestChangeInvalidates(t *testing.T) {
	dir, path := writeInput(t, sampleMP4())
	st, _ := NewStore(filepath.Join(dir, "idx"))
	e1, err := st.GetOrScan(path, probe.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	// Mutate input: append a byte, changing size and digest.
	if err := os.WriteFile(path, append(sampleMP4(), 0x00), 0o600); err != nil {
		t.Fatal(err)
	}
	sum2, err := Summarize(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Lookup(sum2)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("changed summary must invalidate cached entry")
	}
	if e1.Summary.SHA256 == sum2.SHA256 {
		t.Fatal("digest must change after mutation")
	}
	e2, err := st.GetOrScan(path, probe.DefaultLimits())
	if err != nil || e2.Summary.SHA256 != sum2.SHA256 {
		t.Fatalf("rescan after change failed: %v", err)
	}
}

func TestCorruptIndexSelfHeals(t *testing.T) {
	dir, path := writeInput(t, sampleMP4())
	st, _ := NewStore(filepath.Join(dir, "idx"))
	if _, err := st.GetOrScan(path, probe.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	sum, _ := Summarize(path)
	target := st.pathFor(sum)
	if err := os.WriteFile(target, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := st.Lookup(sum)
	if err != nil {
		t.Fatalf("corrupt index must not surface an error: %v", err)
	}
	if got != nil {
		t.Fatal("corrupt entry must be treated as absent")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("corrupt index file should be removed")
	}
}

func TestVersionMismatchInvalidates(t *testing.T) {
	dir, path := writeInput(t, sampleMP4())
	st, _ := NewStore(filepath.Join(dir, "idx"))
	e, err := st.GetOrScan(path, probe.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := Summarize(path)
	e.ScanVersion = "scan-old"
	raw, _ := jsonMarshal(e)
	if err := os.WriteFile(st.pathFor(sum), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := st.Lookup(sum)
	if err != nil || got != nil {
		t.Fatalf("stale scan version must invalidate: %v %v", got, err)
	}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
