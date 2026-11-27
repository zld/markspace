package demux

import (
	"bytes"
	"testing"

	"mediarc/internal/model"
)

func buildTSFixture() []byte {
	video := []TSFrame{
		{PTS: 0, DTS: 0, Payload: append([]byte{0x65}, make([]byte, 200)...), Key: true},
		{PTS: 3000, DTS: 3000, Payload: make([]byte, 250), Key: false},
		{PTS: 6000, DTS: 6000, Payload: append([]byte{0x65}, make([]byte, 300)...), Key: true},
	}
	audio := []TSFrame{
		{PTS: 0, DTS: 0, Payload: []byte{0xAA, 0xBB}},
		{PTS: 2133, DTS: 2133, Payload: []byte{0xCC, 0xDD}},
	}
	return WriteMPEGTS(video, audio)
}

func TestTSDemuxRoundTrip(t *testing.T) {
	data := buildTSFixture()
	tracks, err := NewTSDemuxer("seg0.ts").Demux(data)
	if err != nil {
		t.Fatalf("demux: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks=%d want 2 (video+audio)", len(tracks))
	}
	var video, audio *model.Track
	for i := range tracks {
		switch tracks[i].Codec {
		case model.CodecAVC1:
			video = &tracks[i]
		case model.CodecMP4A:
			audio = &tracks[i]
		}
	}
	if video == nil || audio == nil {
		t.Fatalf("missing codec tracks: %+v", tracks)
	}
	if video.Timescale != 90000 || len(video.Samples) != 3 {
		t.Fatalf("video track wrong: %+v", video)
	}
	// DTS preserved and durations derived from the following sample.
	wantDts := []uint64{0, 3000, 6000}
	for i, s := range video.Samples {
		if s.Dts != wantDts[i] {
			t.Fatalf("dts[%d]=%d want %d", i, s.Dts, wantDts[i])
		}
		if s.Payload[len(s.Payload)-1] != 0 {
			t.Fatalf("payload not preserved byte-for-byte: %x", s.Payload)
		}
	}
	if video.Samples[0].Duration != 3000 || video.Samples[2].Duration != 3000 {
		t.Fatalf("durations wrong: %d %d", video.Samples[0].Duration, video.Samples[2].Duration)
	}
	if len(audio.Samples) != 2 || audio.Samples[1].Dts != 2133 {
		t.Fatalf("audio track wrong: %+v", audio)
	}
	// Each sample must have a mirrored RTP clock packet for recovery.
	if len(video.ClockPackets) != 3 {
		t.Fatal("clock packets missing")
	}
}

func TestTSDemuxPESSpanningPackets(t *testing.T) {
	big := make([]byte, 500)
	big[0] = 0x65
	data := WriteMPEGTS([]TSFrame{{PTS: 0, DTS: 0, Payload: big, Key: true}}, nil)
	tracks, err := NewTSDemuxer("big.ts").Demux(data)
	if err != nil {
		t.Fatalf("demux: %v", err)
	}
	if len(tracks) != 1 || !bytes.Equal(tracks[0].Samples[0].Payload, big) {
		t.Fatalf("multi-packet PES reassembly failed: %+v", tracks)
	}
}

func TestTSDemuxDiscontinuityAndCorruption(t *testing.T) {
	data := buildTSFixture()
	// Flip the sync byte of the third TS packet: the whole fragment fails but
	// the error carries the byte offset.
	corrupt := append([]byte(nil), data...)
	corrupt[2*tsPacketSize] = 0x00
	if _, err := NewTSDemuxer("bad.ts").Demux(corrupt); err == nil {
		t.Fatal("lost sync must be reported")
	} else if me, ok := err.(*model.MediaError); !ok || me.Offset != int64(2*tsPacketSize) {
		t.Fatalf("error offset wrong: %v", err)
	}

	// A trailing partial packet is corruption that still yields the tracks.
	partial := append(data, 0x47, 0x01)
	tracks, err := NewTSDemuxer("partial.ts").Demux(partial)
	if err != nil {
		t.Fatalf("partial trailing packet must not fail the whole fragment: %v", err)
	}
	if len(tracks) == 0 || len(tracks[0].DemuxErrors) == 0 {
		t.Fatal("trailing truncation must be recorded per track")
	}
}

func TestTSDemuxDiscontinuityMarker(t *testing.T) {
	frames := []TSFrame{
		{PTS: 0, DTS: 0, Payload: []byte{0x01}, Key: true},
		{PTS: 90000, DTS: 90000, Payload: []byte{0x02}, Key: true, Discontinuity: true},
	}
	data := WriteMPEGTS(frames, nil)
	tracks, err := NewTSDemuxer("disco.ts").Demux(data)
	if err != nil {
		t.Fatalf("demux: %v", err)
	}
	if len(tracks[0].Samples) != 2 || tracks[0].Samples[1].Dts != 90000 {
		t.Fatalf("discontinuity frame recovery wrong: %+v", tracks[0])
	}
}

func TestTSDemuxGarbage(t *testing.T) {
	if _, err := NewTSDemuxer("g.ts").Demux([]byte{0x00, 0x01}); err == nil {
		t.Fatal("garbage must fail")
	}
}

func TestTSPTSLargeTimestampRoundTrip(t *testing.T) {
	// Values above 2^15 exercise every segment of the 3/15/15 PTS layout,
	// including a full 33-bit carrier value near the wraparound ceiling.
	for _, pts := range []uint64{3000, 90000, 1 << 20, 1<<32 + 12345} {
		frames := []TSFrame{
			{PTS: pts, DTS: pts - 1000, Payload: []byte{0x65, 0x11}, Key: true},
		}
		tracks, err := NewTSDemuxer("pts.ts").Demux(WriteMPEGTS(frames, nil))
		if err != nil {
			t.Fatalf("pts=%d demux: %v", pts, err)
		}
		s := tracks[0].Samples[0]
		if s.Dts != pts-1000 {
			t.Fatalf("pts=%d DTS got %d want %d", pts, s.Dts, pts-1000)
		}
	}
}
