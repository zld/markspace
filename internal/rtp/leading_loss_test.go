package rtp_test

import (
	"testing"

	"github.com/pion/rtp"
	"media-check/internal/fault"
	rtpbuf "media-check/internal/rtp"
)

func TestFirstPacketLost(t *testing.T) {
	fm := fault.NewModel()
	buf := rtpbuf.NewReorderBuffer(1, 1, 8, fm)
	buf.SetExpectedStart(0)
	for seq := uint16(1); seq < 6; seq++ {
		buf.Push(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: seq}})
	}
	out := buf.Flush()
	_, gaps := buf.Stats()
	t.Logf("emitted=%d gaps=%v faults=%d", len(out), gaps, len(fm.All()))
	if len(gaps) != 1 || gaps[0].Start != 0 || gaps[0].End != 0 {
		t.Fatalf("want leading gap [0,0], got %v", gaps)
	}
}
