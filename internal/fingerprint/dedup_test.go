package fingerprint

import (
	"bytes"
	"testing"

	"example.com/continuity/internal/frame"
	"example.com/continuity/internal/segment"
)

func frameRec(seq uint64, w, h uint32, pixels []byte) *frame.Record {
	return &frame.Record{
		Version: frame.Version1, Track: "v", Seq: seq,
		CaptureNS: int64(seq) * 40_000_000, Width: w, Height: h,
		Payload: pixels, Offset: int64(seq) * 64,
	}
}

func segWith(index int, recs ...*frame.Record) *segment.Segment {
	seg := &segment.Segment{
		Index: index, Track: "v", FrameStart: recs[0].Seq,
		FrameEnd: recs[len(recs)-1].Seq + 1,
		StartNS:  recs[0].CaptureNS, EndNS: recs[len(recs)-1].CaptureNS + 40_000_000,
	}
	seg.Frames = recs
	for _, r := range recs {
		seg.PayloadBytes += int64(len(r.Payload))
	}
	return seg
}

func TestNormalizeTrimsPadding(t *testing.T) {
	a := frameRec(0, 2, 2, []byte{1, 2, 3, 4, 9, 9}) // trailing padding
	b := frameRec(1, 2, 2, []byte{1, 2, 3, 4})
	na, err := Normalize(a)
	if err != nil {
		t.Fatal(err)
	}
	nb, err := Normalize(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(na, nb) {
		t.Fatalf("normalization not canonical: %x vs %x", na, nb)
	}
}

func TestDedupKeepsFirstOriginal(t *testing.T) {
	pixels := []byte{10, 20, 30, 40}
	segA := segWith(0, frameRec(0, 2, 2, pixels), frameRec(1, 2, 2, pixels))
	// Same normalized content, different sequence numbers and timestamps.
	segB := segWith(1, frameRec(5, 2, 2, pixels), frameRec(6, 2, 2, pixels))
	// Different content.
	segC := segWith(2, frameRec(9, 2, 2, []byte{1, 1, 1, 1}))

	d := NewDeduper()
	// Observe and classify one segment at a time, mirroring the engine.
	for _, rec := range segA.Frames {
		if _, _, err := d.ObserveFrame(rec); err != nil {
			t.Fatal(err)
		}
	}
	ra, err := d.Apply(segA)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range segB.Frames {
		if _, _, err := d.ObserveFrame(rec); err != nil {
			t.Fatal(err)
		}
	}
	rb, err := d.Apply(segB)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range segC.Frames {
		if _, _, err := d.ObserveFrame(rec); err != nil {
			t.Fatal(err)
		}
	}
	rc, err := d.Apply(segC)
	if err != nil {
		t.Fatal(err)
	}
	if ra.Kind != KindOriginal || rb.Kind != KindDuplicate || rc.Kind != KindOriginal {
		t.Fatalf("kinds: %d %d %d", ra.Kind, rb.Kind, rc.Kind)
	}
	if rb.OriginalIndex != 0 || ra.OriginalIndex != 0 {
		t.Fatalf("original refs: a=%d b=%d", ra.OriginalIndex, rb.OriginalIndex)
	}
	if ra.Digest != rb.Digest || ra.Digest == rc.Digest {
		t.Fatal("digest classification wrong")
	}
	// Duplicate keeps its own time range and input offset.
	if rb.FrameStart != 5 || rb.FrameEnd != 7 || rb.StartNS != 5*40_000_000 || rb.InputOffset != 5*64 {
		t.Fatalf("duplicate provenance lost: %+v", rb)
	}
}

func TestFrameOrderMatters(t *testing.T) {
	segA := segWith(0, frameRec(0, 1, 2, []byte{1, 2}))
	segB := segWith(1, frameRec(0, 1, 2, []byte{2, 1}))
	da, err := SegmentFingerprint(segA)
	if err != nil {
		t.Fatal(err)
	}
	db, err := SegmentFingerprint(segB)
	if err != nil {
		t.Fatal(err)
	}
	if da == db {
		t.Fatal("reordered frames produced identical fingerprints")
	}
}

func TestNormalizeRejectsBadDims(t *testing.T) {
	if _, err := FrameFingerprint(frameRec(0, 0, 2, []byte{1})); err == nil {
		t.Fatal("zero width accepted")
	}
}
