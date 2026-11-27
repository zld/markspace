package ebml

import (
	"testing"

	"mediad/internal/clock"
)

// simpleBlockPayload builds a non-laced SimpleBlock payload.
func simpleBlockPayload(track byte, tc int16, keyframe bool, frame []byte) []byte {
	p := []byte{0x80 | track, byte(uint16(tc) >> 8), byte(uint16(tc))}
	if keyframe {
		p = append(p, 0x80)
	} else {
		p = append(p, 0x00)
	}
	return append(p, frame...)
}

// docWithBlocks builds a two-track segment with interleaved blocks:
// track 1 video (keyframes at tick 0 and 80), track 2 audio.
func docWithBlocks() []byte {
	var doc []byte
	doc = append(doc, elem(IDEBML, []byte{0x01})...)
	var seg []byte
	seg = append(seg, elem(IDInfo, elem(IDTimecodeScale, uintPayload(1_000_000)))...)
	var tracks []byte
	tracks = append(tracks, elem(IDTrackEntry, elem(IDTrackNumber, uintPayload(1)))...)
	tracks = append(tracks, elem(IDTrackEntry, elem(IDTrackNumber, uintPayload(2)))...)
	seg = append(seg, elem(IDTracks, tracks)...)
	// Cluster 1 at base 0: video keyframe @0, audio @0, video @40.
	var c1 []byte
	c1 = append(c1, elem(IDTimecode, uintPayload(0))...)
	c1 = append(c1, elem(IDSimpleBlock, simpleBlockPayload(1, 0, true, []byte{0xAA}))...)
	c1 = append(c1, elem(IDSimpleBlock, simpleBlockPayload(2, 0, false, []byte{0xBB}))...)
	c1 = append(c1, elem(IDSimpleBlock, simpleBlockPayload(1, 40, false, []byte{0xCC}))...)
	seg = append(seg, elem(IDCluster, c1)...)
	// Cluster 2 at base 80, unknown size: video keyframe @80, audio @40.
	var c2 []byte
	c2 = append(c2, elem(IDTimecode, uintPayload(80))...)
	c2 = append(c2, elem(IDSimpleBlock, simpleBlockPayload(1, 0, true, []byte{0xDD}))...)
	c2 = append(c2, elem(IDSimpleBlock, simpleBlockPayload(2, 40, false, []byte{0xEE}))...)
	seg = append(seg, encID(nil, IDCluster)...)
	seg = encUnknown(seg)
	seg = append(seg, c2...)
	doc = append(doc, encID(nil, IDSegment)...)
	doc = encUnknown(doc)
	return append(doc, seg...)
}

func parseSegment(t *testing.T, doc []byte) *Node {
	t.Helper()
	nodes, err := NewParser(doc).Parse()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.ID == IDSegment {
			return n
		}
	}
	t.Fatal("no segment")
	return nil
}

func TestSegmentInfoAndEvents(t *testing.T) {
	seg := parseSegment(t, docWithBlocks())
	scale, tracks, err := SegmentInfo(seg)
	if err != nil {
		t.Fatal(err)
	}
	if scale != 1_000_000 || len(tracks) != 2 {
		t.Fatalf("scale=%d tracks=%v", scale, tracks)
	}
	events, err := Events(seg)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("got %d events, want 5", len(events))
	}
	// Sorted by (tick, offset): ticks 0(v),0(a),40(v),80(v),120(a).
	wantTicks := []int64{0, 0, 40, 80, 120}
	for i, ev := range events {
		if ev.Tick != wantTicks[i] {
			t.Fatalf("event %d tick=%d want %d", i, ev.Tick, wantTicks[i])
		}
		if ev.Offset <= 0 || ev.Size <= 0 {
			t.Fatalf("event %d missing source localization", i)
		}
	}
	if !events[0].Keyframe || events[0].Track != 1 {
		t.Fatalf("first event should be video keyframe: %+v", events[0])
	}
	if events[1].Keyframe {
		t.Fatal("audio block must not be keyframe")
	}
	var keyframes int
	for _, ev := range events {
		if ev.Keyframe {
			keyframes++
		}
	}
	if keyframes != 2 {
		t.Fatalf("got %d keyframes, want 2", keyframes)
	}
}

func TestBlockGroupDuration(t *testing.T) {
	var bg []byte
	bg = append(bg, elem(IDBlock, simpleBlockPayload(1, 5, false, []byte{0x01}))...)
	bg = append(bg, elem(IDBlockDuration, uintPayload(33))...)
	var clu []byte
	clu = append(clu, elem(IDTimecode, uintPayload(100))...)
	clu = append(clu, elem(IDBlockGroup, bg)...)
	var seg []byte
	seg = append(seg, elem(IDTracks, elem(IDTrackEntry, elem(IDTrackNumber, uintPayload(1))))...)
	seg = append(seg, elem(IDCluster, clu)...)
	doc := append(elem(IDEBML, []byte{0x01}), elem(IDSegment, seg)...)

	events, err := Events(parseSegment(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Tick != 105 || events[0].Duration != 33 {
		t.Fatalf("bad event: %+v", events)
	}
	if !events[0].Keyframe {
		t.Fatal("block group without ReferenceBlock is a keyframe")
	}
}

func TestLacedBlockRejected(t *testing.T) {
	p := simpleBlockPayload(1, 0, true, []byte{0x01})
	p[3] = 0x02 // lacing bits set
	var clu []byte
	clu = append(clu, elem(IDTimecode, uintPayload(0))...)
	clu = append(clu, elem(IDSimpleBlock, p)...)
	var seg []byte
	seg = append(seg, elem(IDTracks, elem(IDTrackEntry, elem(IDTrackNumber, uintPayload(1))))...)
	seg = append(seg, elem(IDCluster, clu)...)
	doc := append(elem(IDEBML, []byte{0x01}), elem(IDSegment, seg)...)
	_, err := Events(parseSegment(t, doc))
	if clock.CodeOf(err) != clock.ErrUnsupported {
		t.Fatalf("want ERR_UNSUPPORTED, got %v", err)
	}
}

func TestBadTrackVINT(t *testing.T) {
	p := []byte{0x00, 0x00, 0x00, 0x80, 0x01}
	var clu []byte
	clu = append(clu, elem(IDTimecode, uintPayload(0))...)
	clu = append(clu, elem(IDSimpleBlock, p)...)
	var seg []byte
	seg = append(seg, elem(IDTracks, elem(IDTrackEntry, elem(IDTrackNumber, uintPayload(1))))...)
	seg = append(seg, elem(IDCluster, clu)...)
	doc := append(elem(IDEBML, []byte{0x01}), elem(IDSegment, seg)...)
	_, err := Events(parseSegment(t, doc))
	if clock.CodeOf(err) != clock.ErrInvalidVINT {
		t.Fatalf("want ERR_INVALID_VINT, got %v", err)
	}
}
