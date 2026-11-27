package stream

import (
	"testing"

	"archivecheck/internal/asset"
)

// FuzzAnalyzeMP4 checks that no byte sequence makes the box walker panic or
// report offsets outside the source.
func FuzzAnalyzeMP4(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0, 0, 0, 8},
		{0, 0, 0, 16, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0},
		{0, 0, 0, 0, 'm', 'd', 'a', 't'},
		{0, 0, 0, 1, 'm', 'o', 'o', 'v', 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		src := asset.FromBytes("fuzz.mp4", data)
		s := AnalyzeMP4(src)
		for _, span := range s.Damaged {
			if span.Start < 0 || span.End < span.Start || span.End > src.Size {
				t.Fatalf("damaged span out of range: %+v size=%d", span, src.Size)
			}
		}
		for _, tr := range s.Tracks {
			for _, k := range tr.Keyframes {
				if k.Offset < 0 || k.Offset > src.Size {
					t.Fatalf("keyframe offset out of range: %+v", k)
				}
				if k.Time < 0 {
					t.Fatalf("negative keyframe time: %+v", k)
				}
			}
		}
	})
}

// FuzzAnalyzeRTP checks the capture parser against arbitrary record streams.
func FuzzAnalyzeRTP(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 12, 0x80, 0x60, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3})
	f.Fuzz(func(t *testing.T, data []byte) {
		src := asset.FromBytes("fuzz.rtp", data)
		s := AnalyzeRTP(src)
		for _, span := range s.Damaged {
			if span.Start < 0 || span.End <= span.Start || span.End > src.Size {
				t.Fatalf("damaged span out of range: %+v size=%d", span, src.Size)
			}
		}
		for _, gap := range s.Gaps {
			if gap.Offset < 0 || gap.Offset > src.Size || gap.Missing <= 0 {
				t.Fatalf("bad gap: %+v", gap)
			}
		}
	})
}
