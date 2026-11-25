package segment

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	mp4 "github.com/abema/go-mp4"
)

func TestMuxerWritesInitAndFragment(t *testing.T) {
	dir := t.TempDir()
	m, err := NewMuxer(dir)
	if err != nil {
		t.Fatal(err)
	}
	initName, err := m.WriteInit("high", 27000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, initName)); err != nil {
		t.Fatalf("init file missing: %v", err)
	}
	// No temp files may be left behind after a successful commit.
	if entries, _ := os.ReadDir(dir); len(entries) != 1 || entries[0].Name() != initName {
		t.Fatalf("unexpected directory contents: %v", entries)
	}

	samples := [][]byte{
		{0x00, 0x00, 0x01, 0x09, 0xAA},
		{0x00, 0x00, 0x01, 0x09, 0xBB, 0xCC},
	}
	name, size, err := m.WriteSegment("high", 0, 0, samples)
	if err != nil {
		t.Fatal(err)
	}
	if size <= 0 {
		t.Fatalf("segment size = %d", size)
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}

	// Re-parse the fragment with go-mp4 and require styp/moof/mdat.
	found := map[string]bool{}
	_, err = mp4.ReadBoxStructure(bytes.NewReader(raw), func(h *mp4.ReadHandle) (interface{}, error) {
		found[h.BoxInfo.Type.String()] = true
		return h.Expand()
	})
	if err != nil {
		t.Fatalf("fragment not parseable: %v", err)
	}
	for _, want := range []string{"styp", "moof", "mfhd", "tfhd", "tfdt", "trun", "mdat"} {
		if !found[want] {
			t.Fatalf("fragment missing box %q, have %v", want, found)
		}
	}

	// The init file must be parseable and contain trex.
	initRaw, err := os.ReadFile(filepath.Join(dir, initName))
	if err != nil {
		t.Fatal(err)
	}
	initBoxes := map[string]bool{}
	_, err = mp4.ReadBoxStructure(bytes.NewReader(initRaw), func(h *mp4.ReadHandle) (interface{}, error) {
		initBoxes[h.BoxInfo.Type.String()] = true
		return h.Expand()
	})
	if err != nil {
		t.Fatalf("init not parseable: %v", err)
	}
	for _, want := range []string{"ftyp", "moov", "mvhd", "trak", "trex"} {
		if !initBoxes[want] {
			t.Fatalf("init missing box %q, have %v", want, initBoxes)
		}
	}
}
