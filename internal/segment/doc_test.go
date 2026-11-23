package segment

import "testing"

// TestKeyframeWithinOneFrameOfCut asserts the acceptance rule that every
// segment's recorded keyframe is at most one frame from the segment start.
func TestKeyframeWithinOneFrameOfCut(t *testing.T) {
	src := newTestSource(4000)
	m := buildFixture(t, src)
	frame := m.Config.FrameNanos
	for _, s := range m.Segments {
		delta := s.StartNS - s.KeyframeNS
		if delta < 0 {
			delta = -delta
		}
		if delta > frame {
			t.Fatalf("segment %d keyframe %d ns is %d ns from cut %d (frame %d)",
				s.Index, s.KeyframeNS, delta, s.StartNS, frame)
		}
	}
}
