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
