package damage

import (
	"testing"

	"example.com/media-kernel/codec"
	"example.com/media-kernel/index"
	"example.com/media-kernel/ingest"
	"example.com/media-kernel/media"
)

func TestScannerBuildsAudioGapAndIndexRegions(t *testing.T) {
	sc := NewScanner()
	seq := uint64(9)
	ticks := int64(9000)
	badRange, err := media.NewRange(media.Timebase48k, ticks, 0)
	if err != nil {
		t.Fatal(err)
	}
	audio := []codec.Analyzed{{
		ExtSequence:  seq,
		RTPTimestamp: uint32(ticks),
		Error: media.NewError("codec", media.CodePacketTruncated, "truncated", nil).
			WithSeq(seq).WithRange(&badRange),
	}}
	sc.ScanAudio(audio)
	sc.ScanGap(ingest.Gap{First: 10, Last: 11, Count: 2}, nil)
	sc.ScanIndex(&index.Index{MissingIndex: true})

	report := sc.Report()
	if len(report.Regions) != 3 {
		t.Fatalf("regions=%d want 3: %+v", len(report.Regions), report.Regions)
	}
	if report.MissingPackets != 2 {
		t.Fatalf("missing=%d want 2", report.MissingPackets)
	}
	if report.Regions[0].TimeRange == nil || report.Regions[0].TimeRange.Start.Ticks != ticks {
		t.Fatalf("audio region lost time range: %+v", report.Regions[0])
	}
	kinds := []Kind{report.Regions[0].Kind, report.Regions[1].Kind, report.Regions[2].Kind}
	want := []Kind{KindMalformedPacket, KindMissingPacket, KindMissingIndex}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds=%v want %v", kinds, want)
		}
	}
}

func TestScanTimelineDetectsOverlap(t *testing.T) {
	r1, _ := media.NewRange(media.Timebase48k, 0, 960)
	r2, _ := media.NewRange(media.Timebase48k, 900, 960) // starts before r1 end
	sc := NewScanner()
	sc.ScanTimeline([]codec.Analyzed{{Range: &r1}, {ExtSequence: 2, Range: &r2}})
	if len(sc.Regions()) != 1 || sc.Regions()[0].Kind != KindTimestamp {
		t.Fatalf("regions=%+v want one timestamp anomaly", sc.Regions())
	}
}

func TestScanIndexConvertsCorruptKeyframeError(t *testing.T) {
	br := media.ByteRange{Start: 100, End: 116}
	tr, _ := media.NewRange(media.TimebaseMP4k, 40, 16)
	idx := &index.Index{Keyframes: []index.Keyframe{{
		SampleNumber: 2,
		Error: media.NewError("index", media.CodeKeyframeCorrupt, "bad marker", nil).
			WithByteRange(br.Start, br.End).WithRange(&tr),
	}}}
	sc := NewScanner()
	sc.ScanIndex(idx)
	rs := sc.Regions()
	if len(rs) != 1 || rs[0].ByteRange == nil || *rs[0].ByteRange != br {
		t.Fatalf("regions=%+v want byte range %+v", rs, br)
	}
}
