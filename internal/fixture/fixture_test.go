package fixture

import (
	"encoding/binary"
	"testing"
)

func TestBoxRoundSizes(t *testing.T) {
	b := Box{Type: "moof", Children: []Box{
		{Type: "mfhd", Data: []byte{0, 0, 0, 0, 1, 2, 3, 4}},
	}}
	raw := b.Encode()
	if got := binary.BigEndian.Uint32(raw[0:4]); int(got) != len(raw) {
		t.Fatalf("declared size %d != actual %d", got, len(raw))
	}
	if string(raw[4:8]) != "moof" {
		t.Fatal("type field wrong")
	}
	if hit := findBox(raw, "mfhd"); hit <= 0 {
		t.Fatalf("child lookup failed: %d", hit)
	}
}

func TestMediaSegmentLayout(t *testing.T) {
	samples := []Sample{
		{Duration: 100, Size: 20, Keyframe: true, Payload: 1},
		{Duration: 100, Size: 15, Keyframe: false, Payload: 2},
	}
	raw := MediaSegment(1, 7, 9000, samples)
	if len(raw) == 0 || string(raw[4:8]) != "styp" {
		t.Fatal("segment must start with styp")
	}
	trunAt := findBox(raw, "trun")
	if trunAt < 0 {
		t.Fatal("trun missing")
	}
	dataOff := int32(binary.BigEndian.Uint32(raw[trunAt+16:]))
	moofAt := findBox(raw, "moof")
	mdatAt := findBox(raw, "mdat")
	if int(dataOff) != mdatAt-moofAt+8 {
		t.Fatalf("data offset %d does not point at mdat payload (moof=%d mdat=%d)",
			dataOff, moofAt, mdatAt)
	}
	if tfdtAt := findBox(raw, "tfdt"); tfdtAt < 0 {
		t.Fatal("tfdt missing")
	} else {
		// v1 tfdt: 8-byte base decode time right after full box header.
		if got := binary.BigEndian.Uint64(raw[tfdtAt+12:]); got != 9000 {
			t.Fatalf("base decode time = %d, want 9000", got)
		}
	}
}

func TestInitSegmentContainsTrack(t *testing.T) {
	raw := InitSegment(TrackConfig{TrackID: 2, Timescale: 48000, Width: 320, Height: 240})
	for _, want := range []string{"ftyp", "moov", "mvhd", "trak", "mdhd", "stbl"} {
		if findBox(raw, want) < 0 {
			t.Fatalf("init segment missing box %s", want)
		}
	}
}
