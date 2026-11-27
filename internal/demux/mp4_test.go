package demux

import (
	"bytes"
	"testing"

	"mediarc/internal/model"
)

func buildFixtureMP4(t *testing.T) []byte {
	t.Helper()
	frags := [][]FixtureSample{
		{
			{Dts: 0, Duration: 1000, Payload: []byte{0xDE, 0xAD, 0xBE, 0xEF}, Sync: true},
			{Dts: 1000, Duration: 1000, Payload: []byte{0x01, 0x02, 0x03}},
		},
		{
			{Dts: 2000, Duration: 1000, Payload: []byte{0xCA, 0xFE}, Sync: true},
			{Dts: 3000, Duration: 1000, Payload: []byte{0x09, 0x08, 0x07, 0x06}},
		},
	}
	data, err := WriteFragmentedMP4(1000, 1, "vide", frags)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return data
}

func TestMP4DemuxRoundTrip(t *testing.T) {
	data := buildFixtureMP4(t)
	tracks, err := NewMP4Demuxer("frag.m4s").Demux(data)
	if err != nil {
		t.Fatalf("demux: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("tracks=%d want 1", len(tracks))
	}
	tr := tracks[0]
	if tr.ID != 1 || tr.Timescale != 1000 || tr.Codec != model.CodecAVC1 || tr.NotMappable {
		t.Fatalf("track metadata wrong: %+v", tr)
	}
	if len(tr.Samples) != 4 {
		t.Fatalf("samples=%d want 4", len(tr.Samples))
	}
	wantPayload := [][]byte{{0xDE, 0xAD, 0xBE, 0xEF}, {0x01, 0x02, 0x03}, {0xCA, 0xFE}, {0x09, 0x08, 0x07, 0x06}}
	wantDts := []uint64{0, 1000, 2000, 3000}
	for i, s := range tr.Samples {
		if !bytes.Equal(s.Payload, wantPayload[i]) {
			t.Fatalf("sample %d payload mismatch: got %x want %x", i, s.Payload, wantPayload[i])
		}
		if s.Dts != wantDts[i] || s.Duration != 1000 {
			t.Fatalf("sample %d timing wrong: %+v", i, s)
		}
	}
	if !tr.Samples[0].Sync || tr.Samples[1].Sync {
		t.Fatal("sync flag propagation wrong")
	}
}

func TestMP4DemuxCorrupt(t *testing.T) {
	data := buildFixtureMP4(t)
	// Truncate inside the fragment so a box header or payload is incomplete.
	_, err := NewMP4Demuxer("bad.m4s").Demux(data[:len(data)/2])
	if err == nil {
		t.Fatal("truncated fragment must fail")
	}
	me, ok := err.(*model.MediaError)
	if !ok {
		t.Fatalf("want *MediaError, got %T", err)
	}
	if me.Kind != model.KindSegmentCorrupt || me.File != "bad.m4s" {
		t.Fatalf("error classification wrong: %+v", me)
	}
}

func TestMP4DemuxGarbage(t *testing.T) {
	if _, err := NewMP4Demuxer("g.bin").Demux([]byte("not an mp4 at all")); err == nil {
		t.Fatal("garbage input must fail")
	}
}

func TestMP4DemuxUnknownCodecKept(t *testing.T) {
	frags := [][]FixtureSample{{{Dts: 0, Duration: 512, Payload: []byte{0x77}, Sync: true}}}
	data, err := WriteFragmentedMP4(48000, 2, "hint", frags)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	tracks, err := NewMP4Demuxer("hint.m4s").Demux(data)
	if err != nil {
		t.Fatalf("demux: %v", err)
	}
	if !tracks[0].NotMappable || tracks[0].Codec != model.CodecUnknown {
		t.Fatalf("unknown codec must be preserved but flagged unmappable: %+v", tracks[0])
	}
	if len(tracks[0].Samples) != 1 || tracks[0].Samples[0].Payload[0] != 0x77 {
		t.Fatal("unknown-codec sample bytes must be retained losslessly")
	}
}
