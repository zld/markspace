package pipeline

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"regress-kernel/internal/media"
)

func TestRenderParseSegmentRoundTrip(t *testing.T) {
	fx := media.Generate(media.DefaultConfig("normal", 17))
	frames := fx.Renditions[0].Frames[:FramesPerSegment]
	raw, err := RenderSegment(1, frames[0].TimestampTicks, frames)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	info, err := ParseSegment(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.SequenceNumber != 1 {
		t.Fatalf("sequence number %d want 1", info.SequenceNumber)
	}
	if info.BaseMediaTime != frames[0].TimestampTicks {
		t.Fatalf("base media time %d want %d",
			info.BaseMediaTime, frames[0].TimestampTicks)
	}
	if info.SampleCount != FramesPerSegment {
		t.Fatalf("sample count %d want %d", info.SampleCount, FramesPerSegment)
	}
	for i, f := range frames {
		if info.SampleSizes[i] != uint32(len(f.Payload)) {
			t.Fatalf("sample %d size %d want %d",
				i, info.SampleSizes[i], len(f.Payload))
		}
	}
}

func TestSegmentByteDeterminism(t *testing.T) {
	fx := media.Generate(media.DefaultConfig("normal", 17))
	frames := fx.Renditions[1].Frames[:FramesPerSegment]
	a, err := RenderSegment(2, 1000, frames)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RenderSegment(2, 1000, frames)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(a) != sha256.Sum256(b) {
		t.Fatal("same input rendered different bytes")
	}
	// Different sequence number must yield different content.
	c, _ := RenderSegment(3, 1000, frames)
	if sha256.Sum256(a) == sha256.Sum256(c) {
		t.Fatal("sequence number not encoded into segment")
	}
}

func TestSegmentFramesGrouping(t *testing.T) {
	fx := media.Generate(media.DefaultConfig("normal", 17))
	groups := SegmentFrames(fx.Renditions[0].Frames)
	if len(groups) != 5 { // 50 frames / 10 per segment
		t.Fatalf("got %d groups want 5", len(groups))
	}
	total := 0
	for i, g := range groups {
		if len(g) != FramesPerSegment {
			t.Fatalf("group %d has %d frames", i, len(g))
		}
		total += len(g)
	}
	if total != 50 {
		t.Fatalf("grouping lost frames: %d", total)
	}
	short := SegmentFrames(fx.Renditions[0].Frames[:23])
	if len(short) != 3 || len(short[2]) != 3 {
		t.Fatalf("tail grouping wrong: %+v", lens(short))
	}
}

func lens(groups [][]media.Frame) []int {
	out := make([]int, len(groups))
	for i := range groups {
		out[i] = len(groups[i])
	}
	return out
}

func TestSegmentContiguityAcrossFragments(t *testing.T) {
	fx := media.Generate(media.DefaultConfig("normal", 17))
	var lastBase int64
	var lastSeq uint32
	for i, group := range SegmentFrames(fx.Renditions[0].Frames) {
		raw, err := RenderSegment(uint32(i+1), group[0].TimestampTicks, group)
		if err != nil {
			t.Fatal(err)
		}
		info, err := ParseSegment(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && info.SequenceNumber != lastSeq+1 {
			t.Fatalf("seq discontinuity at %d: %d after %d", i, info.SequenceNumber, lastSeq)
		}
		if i > 0 && info.BaseMediaTime <= lastBase {
			t.Fatalf("time not advancing: %d <= %d", info.BaseMediaTime, lastBase)
		}
		lastBase, lastSeq = info.BaseMediaTime, info.SequenceNumber
	}
}
