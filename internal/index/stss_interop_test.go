package index

import (
	"bytes"
	"testing"

	mp4 "github.com/abema/go-mp4"
)

// TestStandardSTSSInterop proves the pipeline can also read a standards-track
// MP4 sync-sample (stss) box through go-mp4, alongside the custom kidx box.
func TestStandardSTSSInterop(t *testing.T) {
	stss := &mp4.Stss{
		EntryCount:   3,
		SampleNumber: []uint32{1, 7, 13},
	}
	buf := newSeekBuffer()
	mw := mp4.NewWriter(buf)
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
		t.Fatal(err)
	}
	if _, err := mp4.Marshal(mw, stss, mp4.Context{}); err != nil {
		t.Fatal(err)
	}
	if _, err := mw.EndBox(); err != nil {
		t.Fatal(err)
	}

	var samples []uint32
	_, err := mp4.ReadBoxStructure(bytes.NewReader(buf.Bytes()), func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type != mp4.BoxTypeStss() {
			return nil, nil
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		got := box.(*mp4.Stss)
		samples = got.SampleNumber
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 3 || samples[0] != 1 || samples[1] != 7 || samples[2] != 13 {
		t.Fatalf("stss round trip wrong: %v", samples)
	}
}
