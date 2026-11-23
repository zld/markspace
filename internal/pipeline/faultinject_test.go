package pipeline_test

import (
	"encoding/binary"
	"testing"
)

// injectBackwardJump walks the length-prefixed capture and rewrites the
// timestamp of RTP sequence number 1 so it jumps slightly backwards while
// its sequence still advances — the ambiguous rollover condition. Walking
// frames (rather than assuming position) keeps this robust against capture
// reordering.
func injectBackwardJump(t *testing.T, capture []byte) []byte {
	t.Helper()
	out := append([]byte(nil), capture...)
	var off int
	for off+4 <= len(out) {
		frameLen := int(binary.BigEndian.Uint32(out[off : off+4]))
		frameStart := off + 4
		if frameStart+frameLen > len(out) {
			t.Fatal("malformed capture while injecting fault")
		}
		// RTP sequence number is packet byte offset 2..4; timestamp 4..8.
		seq := binary.BigEndian.Uint16(out[frameStart+2 : frameStart+4])
		if seq == 1 {
			tsPos := frameStart + 4
			orig := binary.BigEndian.Uint32(out[tsPos : tsPos+4])
			// Pull the timestamp below the previous packet's value while
			// the sequence still advances: an undecidable backward jump.
			binary.BigEndian.PutUint32(out[tsPos:tsPos+4], orig-4000)
			return out
		}
		off = frameStart + frameLen
	}
	t.Fatal("sequence number 1 not found in capture")
	return nil
}
