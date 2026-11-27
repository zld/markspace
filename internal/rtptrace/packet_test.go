package rtptrace

import (
	"testing"

	"github.com/local/tracecheck/internal/clock"
	"github.com/local/tracecheck/internal/mkv"
	"github.com/pion/rtp"
)

func rtpBlock(t *testing.T, track uint64, seq uint16, ts uint32, off int64, abs clock.Tick) *mkv.Block {
	t.Helper()
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           42,
			Marker:         true,
		},
		Payload: []byte{0xDE, 0xAD},
	}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return &mkv.Block{
		TrackNumber: track, SourceOffset: off, AbsTime: abs, Payload: raw,
	}
}

func TestLossAndJitter(t *testing.T) {
	blocks := []*mkv.Block{
		rtpBlock(t, 1, 10, 160, 100, 0),
		rtpBlock(t, 1, 11, 320, 110, 20_000_000),
		// seq 12 and 13 missing
		rtpBlock(t, 1, 14, 800, 120, 60_000_000),
	}
	tr := FromBlocks(blocks)
	st := tr.Stats[1]
	if st.PacketCount != 3 {
		t.Fatalf("packet count = %d", st.PacketCount)
	}
	if st.LostPackets != 2 || len(st.LossRanges) != 1 {
		t.Fatalf("loss = %+v", st.LossRanges)
	}
	if st.LossRanges[0].Start != 12 || st.LossRanges[0].End != 13 {
		t.Fatalf("loss range = %+v", st.LossRanges[0])
	}
	if st.MaxJitterNS != clock.Tick(40_000_000) {
		t.Fatalf("max jitter = %d", st.MaxJitterNS)
	}
	if tr.TotalLost() != 2 {
		t.Fatalf("total lost = %d", tr.TotalLost())
	}
}

func TestSequenceWrap(t *testing.T) {
	blocks := []*mkv.Block{
		rtpBlock(t, 1, 65535, 0, 1, 0),
		rtpBlock(t, 1, 0, 160, 2, 20_000_000),
		rtpBlock(t, 1, 2, 480, 3, 40_000_000), // one packet lost across wrap
	}
	tr := FromBlocks(blocks)
	if tr.Stats[1].LostPackets != 1 {
		t.Fatalf("wrap loss = %d", tr.Stats[1].LostPackets)
	}
	r := tr.Stats[1].LossRanges[0]
	if r.Start != 1 || r.End != 1 {
		t.Fatalf("wrap loss range = %+v", r)
	}
}

func TestDecodeErrorRecorded(t *testing.T) {
	bad := &mkv.Block{TrackNumber: 1, SourceOffset: 77, AbsTime: 0, Payload: []byte{0x00, 0x01}}
	tr := FromBlocks([]*mkv.Block{bad})
	if len(tr.DecodeErrors) != 1 || tr.DecodeErrors[0].SourceOffset != 77 {
		t.Fatalf("decode errors = %+v", tr.DecodeErrors)
	}
	if tr.Stats[1].PacketCount != 0 {
		t.Fatal("invalid packet must not be counted")
	}
}

func TestMultiTrackIsolation(t *testing.T) {
	blocks := []*mkv.Block{
		rtpBlock(t, 1, 5, 0, 1, 0),
		rtpBlock(t, 2, 9, 0, 2, 0),
		rtpBlock(t, 1, 8, 0, 3, 0), // track 1 loses 6,7; track 2 unaffected
	}
	tr := FromBlocks(blocks)
	if tr.Stats[1].LostPackets != 2 {
		t.Fatalf("track1 loss = %d", tr.Stats[1].LostPackets)
	}
	if tr.Stats[2].LostPackets != 0 {
		t.Fatalf("track2 loss = %d", tr.Stats[2].LostPackets)
	}
}
