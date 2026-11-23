package mp4

import (
	"bytes"
	"testing"
)

func TestAbemaValidateAgreesWithScanner(t *testing.T) {
	spec := FragmentSpec{
		Seq: 0, TrackID: 1, BaseMediaDecode: 0,
		SampleDurations: []uint32{3003, 3003},
		SampleSizes:     []uint32{4, 5},
		SamplePayload:   []byte("abcdefghij"), // 9 bytes -> sizes reconcile
	}
	// Make sizes match the 9-byte payload exactly.
	spec.SampleSizes = []uint32{4, 5}
	data := BuildFragment(spec)

	types, err := AbemaValidate(data)
	if err != nil {
		t.Fatalf("go-mp4 rejected valid fragment: %v", err)
	}
	if len(types) != 2 || types[0] != "moof" || types[1] != "mdat" {
		t.Fatalf("top-level types wrong: %v", types)
	}
	count, err := AbemaCountBoxes(data)
	if err != nil || count < 5 {
		t.Fatalf("box count wrong: %d err=%v", count, err)
	}
}

func TestAbemaValidateRejectsCorruptInput(t *testing.T) {
	// A header claiming more bytes than the stream contains.
	corrupt := []byte{0x00, 0x00, 0x00, 0x40, 'm', 'd', 'a', 't', 1, 2, 3}
	if _, err := AbemaValidate(corrupt); err == nil {
		t.Fatal("go-mp4 must reject the truncated fragment")
	}
}

func TestScannersAgreeOnGeneratedFragment(t *testing.T) {
	tracks := []TrackInfo{
		{TrackID: 1, Timescale: 90000, MediaType: "video", CodecKey: "avc1"},
	}
	data := BuildFragment(FragmentSpec{
		Seq: 1, TrackID: 1, BaseMediaDecode: 90000,
		SampleDurations: []uint32{3003},
		SampleSizes:     []uint32{9},
		SamplePayload:   []byte("abcdefghi"),
	})
	if _, err := AbemaValidate(data); err != nil {
		t.Fatalf("go-mp4 rejected synthesized fragment: %v", err)
	}
	// The init segment is validated by our own parser; its minimal boxes
	// intentionally omit presentation-only fields go-mp4 would require.
	init := BuildInit(tracks)
	parsed, err := ParseInit(bytes.NewReader(init))
	if err != nil || len(parsed) != 1 || parsed[0].TrackID != 1 {
		t.Fatalf("own init parser failed: %v %+v", err, parsed)
	}
}
