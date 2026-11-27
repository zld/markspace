package rtpreplay

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"
)

func TestWriteMP4TwoTracks(t *testing.T) {
	opusDurs := []uint32{960, 960, 960}
	opusSizes := []uint32{40, 40, 40}
	aacDurs := []uint32{1024, 1024, 1024, 1024}
	aacSizes := []uint32{60, 60, 60, 60}
	data, err := WriteMP4Tracks([]AudioTrackSpec{
		{Codec: CodecOpus, Timescale: 48000, Durations: opusDurs, Sizes: opusSizes,
			OpusPreSkip: 312, OpusRate: 48000, SampleRate: 48000, Channels: 2},
		{Codec: CodecAAC, Timescale: 48000, Durations: aacDurs, Sizes: aacSizes,
			AACASC: []byte{0x11, 0x90}, SampleRate: 48000, Channels: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	tracks, err := ReadMP4Tracks(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks=%d", len(tracks))
	}
	byCodec := map[Codec]*MP4Track{}
	for _, tr := range tracks {
		byCodec[tr.Codec] = tr
	}
	op := byCodec[CodecOpus]
	aa := byCodec[CodecAAC]
	if op == nil || len(op.Samples) != 3 || op.Samples[0].Duration != 960 {
		t.Fatalf("opus=%+v", op)
	}
	if aa == nil || len(aa.Samples) != 4 || aa.Samples[0].Duration != 1024 {
		t.Fatalf("aac=%+v", aa)
	}
	if aa.AAC == nil || aa.AAC.SampleRate != 48000 {
		t.Fatalf("aac cfg=%+v", aa.AAC)
	}
	if aa.TrackID != 2 {
		t.Fatalf("aac trackID=%d", aa.TrackID)
	}
}

// TestWriteMP4TracksChunkOffsetsPerTrack guards the multi-track stco patch:
// each track's chunk offsets must point into its own mdat region, not the
// first track's, or a strict container reader would read the wrong bytes.
func TestWriteMP4TracksChunkOffsetsPerTrack(t *testing.T) {
	data, err := WriteMP4Tracks([]AudioTrackSpec{
		{Codec: CodecOpus, Timescale: 48000, Durations: []uint32{960, 960}, Sizes: []uint32{10, 20},
			OpusPreSkip: 312, OpusRate: 48000, SampleRate: 48000, Channels: 2},
		{Codec: CodecAAC, Timescale: 48000, Durations: []uint32{1024, 1024}, Sizes: []uint32{30, 40},
			AACASC: []byte{0x11, 0x90}, SampleRate: 48000, Channels: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	var stcos []*mp4.Stco
	if _, err := mp4.ReadBoxStructure(bytes.NewReader(data), func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type == mp4.BoxTypeStco() {
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			stcos = append(stcos, box.(*mp4.Stco))
			return nil, nil
		}
		_, err := h.Expand()
		return nil, err
	}); err != nil {
		t.Fatal(err)
	}
	if len(stcos) != 2 {
		t.Fatalf("stco boxes=%d", len(stcos))
	}
	for i, want := range [][]uint32{{10, 20}, {30, 40}} {
		if len(stcos[i].ChunkOffset) != len(want) {
			t.Fatalf("stco[%d] entries=%v", i, stcos[i].ChunkOffset)
		}
		// Consecutive samples of one track are 4-byte aligned within mdat.
		for j := 1; j < len(want); j++ {
			gap := stcos[i].ChunkOffset[j] - stcos[i].ChunkOffset[j-1]
			if gap < want[j-1] || gap > want[j-1]+3 {
				t.Fatalf("stco[%d][%d] delta=%d for size %d", i, j, gap, want[j-1])
			}
		}
	}
	if stcos[0].ChunkOffset[0] >= stcos[1].ChunkOffset[0] {
		t.Fatalf("track 2 offsets must follow track 1: %v vs %v", stcos[0].ChunkOffset, stcos[1].ChunkOffset)
	}
}
