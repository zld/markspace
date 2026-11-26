package ebml

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"pwflow/internal/fault"
)

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func ebmlUint(v uint64) []byte {
	var buf [8]byte
	n := 0
	for x := v; ; x >>= 8 {
		buf[7-n] = byte(x)
		n++
		if x < 256 {
			break
		}
	}
	return buf[8-n:]
}

func ebmlElem(id uint32, payload []byte) []byte {
	out := append([]byte{}, EncodeID(id)...)
	out = append(out, EncodeSize(uint64(len(payload)))...)
	return append(out, payload...)
}

func ebmlUnknownElem(id uint32, width int, payload []byte) []byte {
	out := append([]byte{}, EncodeID(id)...)
	out = append(out, EncodeUnknownSize(width)...)
	return append(out, payload...)
}

func ebmlBlock(track uint64, rel int16, flags byte, payload []byte) []byte {
	out := []byte{0x80 | byte(track)} // test tracks 1..126 use one-byte VINT
	var tc [2]byte
	binary.BigEndian.PutUint16(tc[:], uint16(rel))
	out = append(out, tc[:]...)
	out = append(out, flags)
	out = append(out, payload...)
	return out
}

func TestParseUnknownSegmentAndInterleavedTracks(t *testing.T) {
	info := ebmlElem(IDInfo, ebmlElem(IDTimecodeScale, ebmlUint(1_000_000)))
	videoTrack := ebmlElem(IDTrackEntry, cat(
		ebmlElem(IDTrackNumber, ebmlUint(1)),
		ebmlElem(IDTrackType, ebmlUint(1)),
		ebmlElem(IDCodecID, []byte("V_VP9")),
		ebmlElem(IDVideo, cat(ebmlElem(IDPixelWidth, ebmlUint(320)), ebmlElem(IDPixelHeight, ebmlUint(240)))),
	))
	audioTrack := ebmlElem(IDTrackEntry, cat(
		ebmlElem(IDTrackNumber, ebmlUint(2)),
		ebmlElem(IDTrackType, ebmlUint(2)),
		ebmlElem(IDCodecID, []byte("A_OPUS")),
	))
	tracks := ebmlElem(IDTracks, append(videoTrack, audioTrack...))

	c1Payload := ebmlElem(IDTimecode, ebmlUint(1000))
	c1Payload = append(c1Payload, ebmlElem(IDSimpleBlock, ebmlBlock(1, 0, 0x80, []byte("video-key")))...)
	c1Payload = append(c1Payload, ebmlElem(IDSimpleBlock, ebmlBlock(2, 5, 0x00, []byte("audio")))...)
	c1Payload = append(c1Payload, ebmlElem(IDSimpleBlock, ebmlBlock(1, 33, 0x00, []byte("video-delta")))...)
	// A structurally truncated block. Its declared one-byte payload cannot
	// contain a block header; recovery must not trust its declared range.
	bad := ebmlElem(IDSimpleBlock, []byte{0x81})
	c1Payload = append(c1Payload, bad...)

	c2Payload := cat(
		ebmlElem(IDTimecode, ebmlUint(2000)),
		ebmlElem(IDSimpleBlock, ebmlBlock(1, 0, 0x80, []byte("next-key"))),
	)
	cluster1 := ebmlUnknownElem(IDCluster, 4, c1Payload)
	cluster2 := ebmlElem(IDCluster, c2Payload)

	segmentPayload := append(info, tracks...)
	segmentPayload = append(segmentPayload, cluster1...)
	segmentPayload = append(segmentPayload, cluster2...)
	doc := ebmlElem(IDEBMLHeader, ebmlElem(0x4282, []byte("webm")))
	segment := ebmlUnknownElem(IDSegment, 1, segmentPayload)

	rep, err := Parse(append(doc, segment...), DefaultLimits())
	if err != nil {
		t.Fatalf("Parse returned fatal error: %v", err)
	}
	if !rep.UnknownSegment || rep.UnknownClusters != 1 {
		t.Fatalf("unknown segment=%v clusters=%d", rep.UnknownSegment, rep.UnknownClusters)
	}
	if len(rep.Tracks) != 2 || rep.Tracks[0].Width != 320 || rep.Tracks[1].Codec != "A_OPUS" {
		t.Fatalf("tracks not parsed: %+v", rep.Tracks)
	}
	if len(rep.Blocks) != 4 { // three before bad block, one in confirmed next cluster
		t.Fatalf("blocks = %d (%+v), want 4 parsed blocks", len(rep.Blocks), rep.Blocks)
	}
	if rep.Blocks[0].PTSNS != 1_000_000_000 || !rep.Blocks[0].Keyframe {
		t.Fatalf("first video block mapping/keyframe wrong: %+v", rep.Blocks[0])
	}
	if rep.Blocks[1].PTSNS != 1_005_000_000 || rep.Blocks[1].TrackNumber != 2 {
		t.Fatalf("interleaved audio block mapping wrong: %+v", rep.Blocks[1])
	}
	last := rep.Blocks[len(rep.Blocks)-1]
	if last.ClusterTimecodeNS != 2_000_000_000 || last.PTSNS != 2_000_000_000 {
		t.Fatalf("next cluster block not mapped: %+v", last)
	}
	if len(rep.Damages) != 1 {
		t.Fatalf("damages = %+v, want one", rep.Damages)
	}
	d := rep.Damages[0]
	if d.Code != fault.EBMLBadBlock || d.Offset < 0 || d.SkippedTo <= d.Offset {
		t.Fatalf("bad damage record: %+v", d)
	}
	if d.SkippedTo <= d.Offset || d.SkippedTo >= last.ElementOffset {
		t.Fatalf("resync boundary %d should be between bad block %d and next block %d",
			d.SkippedTo, d.Offset, last.ElementOffset)
	}
}

func TestParseStopsWhenNextClusterUnconfirmed(t *testing.T) {
	payload := ebmlElem(IDTimecode, ebmlUint(10))
	payload = append(payload, ebmlElem(IDSimpleBlock, ebmlBlock(1, 0, 0x80, []byte("ok")))...)
	payload = append(payload, ebmlElem(IDSimpleBlock, []byte{0x81})...)
	cluster := ebmlUnknownElem(IDCluster, 1, payload)
	segment := ebmlUnknownElem(IDSegment, 1, cluster)

	rep, err := Parse(segment, DefaultLimits())
	if err == nil {
		t.Fatal("expected fatal resync failure")
	}
	if !fault.IsCode(err, fault.EBMLResyncFailed) {
		t.Fatalf("error code = %v, want %s", err, fault.EBMLResyncFailed)
	}
	if rep == nil || len(rep.Blocks) != 1 {
		t.Fatalf("report should retain pre-damage block: %+v", rep)
	}
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Offset < 0 {
		t.Fatalf("expected anchored fault error, got %v", err)
	}
}

func TestParseRejectsTooManyTracks(t *testing.T) {
	tracksPayload := []byte{}
	for i := 1; i <= 3; i++ {
		tracksPayload = append(tracksPayload, ebmlElem(IDTrackEntry, ebmlElem(IDTrackNumber, ebmlUint(uint64(i))))...)
	}
	segment := ebmlElem(IDSegment, ebmlElem(IDTracks, tracksPayload))
	limits := DefaultLimits()
	limits.MaxTracks = 2
	_, err := Parse(segment, limits)
	if !fault.IsCode(err, fault.TooManyTracks) {
		t.Fatalf("err=%v, want TooManyTracks", err)
	}
}
