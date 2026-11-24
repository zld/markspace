package manifest

import (
	"bytes"
	"os"
	"testing"

	mp4 "github.com/abema/go-mp4"
)

func TestWriteAndReadBoxes(t *testing.T) {
	m := Metadata{
		JobID: "job-1", SourceRate: 48000, TargetRate: 48000,
		Layout: "stereo", TargetLUFS: -16, Packets: 12029, Gaps: 3,
		SamplesIn: 1151712, SamplesOut: 1151712,
		SHA256: "f7ee51ea11cba2c42011a4f7794f0205756ad73a2",
	}
	payload := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	f, err := os.CreateTemp("", "manifest-*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if err := WriteBoxes(f, m, payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("no bytes written")
	}

	// Read back and confirm the expected top-level box types are present.
	var seen []string
	r := bytes.NewReader(raw)
	_, err = mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		seen = append(seen, h.BoxInfo.Type.String())
		switch h.BoxInfo.Type {
		case mp4.BoxTypeUdta():
			var got bytes.Buffer
			if _, err := h.ReadData(&got); err != nil {
				return nil, err
			}
			if got.String() != string(encodeMetadata(m)) {
				t.Errorf("udta payload mismatch:\n got %s\nwant %s", got.String(), encodeMetadata(m))
			}
			return nil, nil // raw JSON bytes, not nested boxes
		case mp4.BoxTypeMdat():
			return nil, nil // opaque media payload
		default:
			return h.Expand() // ftyp, moov are real containers
		}
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := map[string]bool{"ftyp": false, "moov": false, "mdat": false}
	for _, s := range seen {
		if _, ok := want[s]; ok {
			want[s] = true
		}
	}
	for box, found := range want {
		if !found {
			t.Errorf("box %q missing, saw %v", box, seen)
		}
	}
}
