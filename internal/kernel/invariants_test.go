package kernel

import "testing"

// TestCheckKeyframeBytesRejectsNonKeyframe builds a real SimpleBlock with the
// keyframe flag clear and confirms the invariant check rejects it. This proves
// the verifier inspects bytes rather than trusting the recorded index.
func TestCheckKeyframeBytesRejectsNonKeyframe(t *testing.T) {
	// SimpleBlock A3 | size 0x85 | track 0x81 | timecode 00 00 | flags 00 | 'a'
	block := []byte{0xA3, 0x85, 0x81, 0x00, 0x00, 0x00, 'a'}
	seg := segmentForTest(block)
	if err := checkKeyframeBytes(seg, block); err == nil {
		t.Fatal("non-keyframe block accepted as keyframe")
	}
	// Same block with the keyframe flag set must pass.
	key := append([]byte{}, block...)
	key[5] = 0x80
	if err := checkKeyframeBytes(segmentForTest(key), key); err != nil {
		t.Fatalf("real keyframe rejected: %v", err)
	}
}
