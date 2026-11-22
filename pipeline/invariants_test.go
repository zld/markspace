package pipeline

import (
	"bytes"
	"testing"

	"mediacore/index"
	"mediacore/media"
)

// TestSegmentsPartitionFramesExactly ensures every indexed frame lands in
// exactly one segment, with no gaps or double counting at boundaries.
func TestSegmentsPartitionFramesExactly(t *testing.T) {
	cases := []struct {
		name  string
		n     int
		quiet func(int) *float64
		loss  []int
	}{
		{"plain", 250, nil, nil},
		{"boundary", 101, nil, nil},
		{"quiet", 200, func(i int) *float64 {
			if i%40 < 10 {
				return volume(-70)
			}
			return volume(-5)
		}, nil},
		{"withloss", 300, nil, []int{100, 101, 102, 150}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames := timeline(tc.n, tc.quiet)
			if tc.loss != nil {
				drop := map[int]bool{}
				for _, i := range tc.loss {
					drop[i] = true
				}
				kept := frames[:0]
				for i, f := range frames {
					if !drop[i] {
						kept = append(kept, f)
					}
				}
				frames = kept
			}
			data, err := media.EncodeStream(frames)
			if err != nil {
				t.Fatal(err)
			}
			idx, err := index.Scan(bytes.NewReader(data), index.DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			seg, _ := NewSegmenter(DefaultSegmentConfig(media.DefaultTimescale))
			segments, _ := seg.Segments(idx.Entries)
			total := 0
			for _, s := range segments {
				total += s.FrameCount
			}
			if total != len(idx.Entries) {
				t.Fatalf("frames partitioned=%d want %d", total, len(idx.Entries))
			}
			// Verify first/last sequence coverage matches.
			if segments[0].FirstSeq != idx.Entries[0].Seq {
				t.Fatalf("first seq=%d want %d", segments[0].FirstSeq, idx.Entries[0].Seq)
			}
			if segments[len(segments)-1].LastSeq != idx.Entries[len(idx.Entries)-1].Seq {
				t.Fatalf("last seq mismatch")
			}
		})
	}
}

// TestCountMissingHandlesBackwardAndWrap guards against inflating a reordered
// or wrapped sequence step into a full-window of missing frames.
func TestCountMissingHandlesBackwardAndWrap(t *testing.T) {
	cases := []struct {
		prev, next uint32
		want       int
	}{
		{100, 101, 0},
		{100, 150, 49},
		{500, 499, 0}, // backward step is not a gap
		{65535, 0, 0}, // wrap, no gap
		{65534, 1, 2}, // wrap spanning two missing
		{7, 7, 0},
	}
	for _, c := range cases {
		if got := countMissing(c.prev, c.next); got != c.want {
			t.Fatalf("countMissing(%d,%d)=%d want %d", c.prev, c.next, got, c.want)
		}
	}
}

// TestSilenceAttributionMatchesTotal ensures per-segment silence sums equal the
// confirmed total even when an interval spans a segment boundary. It goes
// through the real manifest, not a copy of the overlap logic.
func TestSilenceAttributionMatchesTotal(t *testing.T) {
	// 300 frames of 40 ms = 12 s; quiet from frame 90 to 140 crosses the 4 s
	// and 8 s boundaries and also sits inside the second segment.
	frames := timeline(300, func(i int) *float64 {
		if i >= 90 && i < 140 {
			return volume(-70)
		}
		return volume(-6)
	})
	data, idx := buildStream(t, frames)
	cfg := DefaultConfig(media.DefaultTimescale)
	res, err := Run(idx, NewBufferSource(data, media.DefaultLimits()), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var perSegment int64
	confirmed := Confirmed(res.Silence)
	for _, s := range res.Segments {
		start, end := s.StartMS, s.EndMS
		for _, iv := range confirmed {
			a, b := iv.StartMS, iv.EndMS
			if a < start {
				a = start
			}
			if b > end {
				b = end
			}
			if b > a {
				perSegment += b - a
			}
		}
	}
	if perSegment != res.SilenceConfirmedMS {
		t.Fatalf("per-segment silence=%dms total=%dms (double count or loss)", perSegment, res.SilenceConfirmedMS)
	}
}
