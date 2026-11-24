package fingerprint

import (
	"testing"

	"mediapipe/internal/audio"
	"mediapipe/internal/model"
)

func fpPacket(seq uint64, ms int64, amp int16) model.Packet {
	samples := make([]int16, 32)
	for i := range samples {
		samples[i] = amp
	}
	return model.Packet{
		ExtSequence:  seq,
		MediaMs:      ms,
		FrameMs:      20,
		Payload:      audio.EncodePayload(samples, 8000),
		PayloadValid: true,
	}
}

func TestWindowHashDeterministicAndContentSensitive(t *testing.T) {
	segA := model.Segment{Index: 0, StartMs: 0, EndMs: 40}
	pkA := []model.Packet{fpPacket(1, 0, 900), fpPacket(2, 20, 1200)}
	a1 := HashWindows(segA, pkA, 20)
	a2 := HashWindows(segA, pkA, 20)
	if len(a1) != 2 || a1[0].Hash != a2[0].Hash || a1[1].Hash != a2[1].Hash {
		t.Fatalf("hashes not deterministic: %+v vs %+v", a1, a2)
	}

	segB := model.Segment{Index: 4, StartMs: 8000, EndMs: 8040}
	pkB := []model.Packet{fpPacket(90, 8000, 900), fpPacket(91, 8020, 1200)}
	b := HashWindows(segB, pkB, 20)
	if a1[0].Hash != b[0].Hash || a1[1].Hash != b[1].Hash {
		t.Fatal("same relative content at another media time must hash equally")
	}

	pkC := []model.Packet{fpPacket(1, 0, 901), fpPacket(2, 20, 1200)}
	c := HashWindows(segA, pkC, 20)
	if a1[0].Hash == c[0].Hash {
		t.Fatal("content change did not change fingerprint")
	}
}

func TestDeduperMapsMergedToKept(t *testing.T) {
	d := NewDeduper(20)
	segA := model.Segment{Index: 0, StartMs: 0, EndMs: 40}
	pkA := []model.Packet{fpPacket(1, 0, 700), fpPacket(2, 20, 700)}
	fpA := HashWindows(segA, pkA, 20)
	if _, m := d.Add(segA, fpA); m != nil {
		t.Fatal("first segment should be kept")
	}

	segB := model.Segment{Index: 3, StartMs: 6000, EndMs: 6040}
	pkB := []model.Packet{fpPacket(40, 6000, 700), fpPacket(41, 6020, 700)}
	fpB := HashWindows(segB, pkB, 20)
	out, m := d.Add(segB, fpB)
	if m == nil {
		t.Fatal("expected duplicate")
	}
	if out.MergedInto == nil || *out.MergedInto != 0 {
		t.Fatalf("merged mapping = %v", out.MergedInto)
	}
	if m.KeptSegment != 0 || m.MergedSegment != 3 || m.MatchingWindows != 2 {
		t.Fatalf("bad mapping: %+v", m)
	}
	if d.Duplicate != 1 || len(d.MappingsSnapshot()) != 1 {
		t.Fatalf("deduper state duplicate=%d mappings=%d", d.Duplicate, len(d.Mappings))
	}
}

func TestDeduperDoesNotMergeIncompleteWindows(t *testing.T) {
	d := NewDeduper(20)
	seg := model.Segment{Index: 0, StartMs: 0, EndMs: 40}
	fps := HashWindows(seg, []model.Packet{fpPacket(1, 0, 700)}, 20)
	if _, m := d.Add(seg, fps); m != nil {
		t.Fatal("incomplete first segment should not create a duplicate relation")
	}
	if len(d.Mappings) != 0 {
		t.Fatalf("mappings=%d", len(d.Mappings))
	}
}
