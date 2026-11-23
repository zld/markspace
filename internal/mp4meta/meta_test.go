package mp4meta

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"
)

func doc() Document {
	return Document{Tracks: []Track{
		{ID: 1, Timescale: 30000, Times: []int64{0, 1500, 3000, 4500}, Bytes: []int64{100, 200, 150, 250}, Keyframes: []int{0, 2}},
		{ID: 2, Timescale: 90000, Times: []int64{0, 4500, 9000}, Bytes: []int64{500, 600, 700}, Keyframes: []int{0}, Handler: [4]byte{'v', 'i', 'd', 'e'}},
	}}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	b, err := Encode(doc())
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 || string(b[4:8]) != "ftyp" {
		t.Fatalf("missing ftyp header: %q", b[:8])
	}
	metas, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Fatalf("tracks=%d", len(metas))
	}
	first := metas[0]
	if first.TrackID != 1 || first.Timescale != 30000 {
		t.Fatalf("first=%+v", first)
	}
	if first.SampleCount != 4 || first.TotalBytes != 700 {
		t.Fatalf("first counts=%+v", first)
	}
	if len(first.KeyframeTimes) != 2 || first.KeyframeTimes[1] != 3000 {
		t.Fatalf("keyframe times=%+v", first.KeyframeTimes)
	}
	if len(first.KeyframeFrames) != 2 || first.KeyframeFrames[1] != 2 {
		t.Fatalf("keyframe frames=%+v", first.KeyframeFrames)
	}
	second := metas[1]
	if second.Timescale != 90000 || second.TotalBytes != 1800 || second.Duration != 9000 {
		t.Fatalf("second=%+v", second)
	}
	if second.Handler != [4]byte{'v', 'i', 'd', 'e'} {
		t.Fatalf("handler=%q", second.Handler)
	}
}

func TestEncodeIsDeterministic(t *testing.T) {
	a, err := Encode(doc())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encode(doc())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("metadata bytes differ between identical inputs")
	}
}

func TestEncodeRejectsBadTracks(t *testing.T) {
	cases := []Document{
		{},
		{Tracks: []Track{{Timescale: 0, Times: []int64{0}}}},
		{Tracks: []Track{{Timescale: 1}}},
		{Tracks: []Track{{Timescale: 1, Times: []int64{0, 10}, Bytes: []int64{1}}}},
		{Tracks: []Track{{Timescale: 1, Times: []int64{0, 10}, Bytes: []int64{1, 1}, Keyframes: []int{5}}}},
		{Tracks: []Track{{Timescale: 1, Times: []int64{0, 10}, Bytes: []int64{1, 1}, Keyframes: []int{-1}}}},
		{Tracks: []Track{{Timescale: 1, Times: []int64{10, 0}, Bytes: []int64{1, 1}}}},
	}
	for i, d := range cases {
		if _, err := Encode(d); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := Decode([]byte("not an mp4")); err == nil {
		t.Fatal("garbage accepted")
	}
	// A well-formed file with no trak boxes.
	var buf bytes.Buffer
	w := mp4.NewWriter(&seekWriter{buf: &buf})
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.EndBox(); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(buf.Bytes()); err == nil {
		t.Fatal("trackless document accepted")
	}
}

func TestEmptyTimesRunEncoded(t *testing.T) {
	// A single-sample track needs a zero-length delta run.
	b, err := Encode(Document{Tracks: []Track{{Timescale: 1000, Times: []int64{0}, Bytes: []int64{10}, Keyframes: []int{0}}}})
	if err != nil {
		t.Fatal(err)
	}
	metas, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if metas[0].SampleCount != 1 || len(metas[0].KeyframeTimes) != 1 {
		t.Fatalf("meta=%+v", metas[0])
	}
}
