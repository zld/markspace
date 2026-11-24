package medpipe

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	mp4 "github.com/abema/go-mp4"
)

func TestWriteTFRARoundTrip(t *testing.T) {
	digest, _ := HashReader(bytes.NewReader([]byte("mp4-input")))
	idx := sampleIndex(t, "v", digest)

	// bytes.Buffer has no Seek; use a temp file like the real pipeline.
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "v.mfra"))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteTFRA(f, 1, &idx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	boxes, err := mp4.ExtractBoxes(f, nil, []mp4.BoxPath{
		{mp4.BoxTypeMfra(), mp4.BoxTypeTfra()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 1 {
		t.Fatalf("want one tfra box, got %d", len(boxes))
	}
	// Re-read with payload to verify entry count and time mapping.
	tfraPayloads, err := mp4.ExtractBoxesWithPayload(f, nil, []mp4.BoxPath{
		{mp4.BoxTypeMfra(), mp4.BoxTypeTfra()},
	})
	if err != nil {
		t.Fatal(err)
	}
	tfra := tfraPayloads[0].Payload.(*mp4.Tfra)
	if tfra.NumberOfEntry != uint32(idx.KeyframeCount) {
		t.Fatalf("tfra entries = %d, want %d", tfra.NumberOfEntry, idx.KeyframeCount)
	}
	if tfra.Entries[1].TimeV1 != 66667 || tfra.Entries[1].MoofOffsetV1 != 36 {
		t.Fatalf("tfra entry mapping wrong: %+v", tfra.Entries[1])
	}
}

func TestWriteMoofStructureAndDataOffset(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "frag.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	frag := Fragment{
		Sequence:     7,
		TrackID:      2,
		DecodeTimeUS: 100000,
		Samples: []FragmentSample{
			{DurationUS: 33333, Size: 100, IsKeyframe: true},
			{DurationUS: 33333, Size: 80, IsKeyframe: false},
		},
	}
	const payload = uint64(180)
	if err := WriteMoof(f, frag, payload); err != nil {
		t.Fatal(err)
	}
	// Append the actual mdat payload and close.
	if _, err := f.Write(make([]byte, payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	// Extract moof children and verify tfdt time plus trun data offset.
	payloads, err := mp4.ExtractBoxesWithPayload(f, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTfdt()},
		{mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTrun()},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sawTfdt, sawTrun bool
	for _, p := range payloads {
		switch box := p.Payload.(type) {
		case *mp4.Tfdt:
			if box.GetBaseMediaDecodeTime() != 100000 {
				t.Fatalf("tfdt decode time = %d", box.GetBaseMediaDecodeTime())
			}
			sawTfdt = true
		case *mp4.Trun:
			if box.SampleCount != 2 || box.DataOffset <= 8 {
				t.Fatalf("trun header wrong: count=%d offset=%d", box.SampleCount, box.DataOffset)
			}
			if box.Entries[0].SampleSize != 100 || box.Entries[1].SampleFlags == 0 {
				t.Fatalf("trun sample rows wrong: %+v", box.Entries)
			}
			sawTrun = true
		}
	}
	if !sawTfdt || !sawTrun {
		t.Fatal("moof missing tfdt or trun")
	}
}
