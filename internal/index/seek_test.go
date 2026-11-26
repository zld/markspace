package index

import (
	"testing"

	"replayproof/internal/media"
)

func TestMarshalIsStable(t *testing.T) {
	asset, _ := testAsset()
	idx, _ := Build(asset, nil)
	a, err := idx.Marshal("mp4", map[uint32]uint32{1: 90000, 2: 48000})
	if err != nil {
		t.Fatal(err)
	}
	b, err := idx.Marshal("mp4", map[uint32]uint32{1: 90000, 2: 48000})
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("index output is not deterministic")
	}
	doc, err := ParseJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	if doc.SampleCount() != 32 {
		t.Fatalf("sample count=%d", doc.SampleCount())
	}
	lo, hi, ok := doc.ByteExtent()
	// Video occupies 1000..2600, audio 5000..5800.
	if !ok || lo != 1000 || hi != 5800 {
		t.Fatalf("extent=%d..%d ok=%v", lo, hi, ok)
	}
	if doc.Tracks[0].Entries[0].DTS != 0 || doc.Tracks[0].Entries[1].DTS != 33333 {
		t.Fatalf("dts=%#v", doc.Tracks[0].Entries[:2])
	}
}

func TestParseJSONRejectsBadVersion(t *testing.T) {
	if _, err := ParseJSON([]byte(`{"version":99,"source":"x","tracks":[]}`)); err == nil {
		t.Fatal("expected version error")
	}
	if _, err := ParseJSON([]byte(`not json`)); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestSeekConsistencyAfterJSONRoundTrip(t *testing.T) {
	asset, _ := testAsset()
	idx, _ := Build(asset, nil)
	data, _ := idx.Marshal("mp4", nil)
	doc, _ := ParseJSON(data)
	// Rebuild entries from JSON and confirm the same keyframe is selected.
	rebuilt := &Index{}
	for _, t := range doc.Tracks {
		ti := &TrackIndex{TrackID: t.TrackID}
		for _, e := range t.Entries {
			ti.Entries = append(ti.Entries, Entry{
				TrackID: e.TrackID, Number: e.Number,
				DTS: media.FromMicros(e.DTS), End: media.FromMicros(e.End),
				PTS: media.FromMicros(e.PTS), Offset: e.Offset, Size: e.Size, Keyframe: e.Key,
			})
		}
		rebuilt.Tracks = append(rebuilt.Tracks, ti)
	}
	want, _ := idx.Track(1).Seek(media.FromMicros(500_000))
	got, err := rebuilt.Track(1).Seek(media.FromMicros(500_000))
	if err != nil {
		t.Fatal(err)
	}
	if got.Entry.Number != want.Entry.Number || got.Entry.Offset != want.Entry.Offset {
		t.Fatalf("seek drift %#v vs %#v", got.Entry, want.Entry)
	}
}
