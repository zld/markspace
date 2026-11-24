package ingest

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/pion/rtp"

	"mediad/internal/mederr"
)

func appendFramedRTP(t *testing.T, buf *bytes.Buffer, arrival int64, seq uint16, ts uint32, payload []byte) {
	t.Helper()
	pkt := rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 1},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	hdr := make([]byte, 12)
	binary.BigEndian.PutUint32(hdr[0:4], uint32(len(raw)))
	binary.BigEndian.PutUint64(hdr[4:12], uint64(arrival))
	buf.Write(hdr)
	buf.Write(raw)
}

func appendRawFrame(buf *bytes.Buffer, arrival int64, raw []byte) {
	hdr := make([]byte, 12)
	binary.BigEndian.PutUint32(hdr[0:4], uint32(len(raw)))
	binary.BigEndian.PutUint64(hdr[4:12], uint64(arrival))
	buf.Write(hdr)
	buf.Write(raw)
}

func TestReadRTPStreamBadPacketAndNonMonotonicAreLocated(t *testing.T) {
	var buf bytes.Buffer
	appendFramedRTP(t, &buf, 0, 1, 1000, []byte("ok"))
	appendRawFrame(&buf, 20_000_000, []byte{0xff, 0xff, 0x01}) // too short/invalid RTP
	appendFramedRTP(t, &buf, 40_000_000, 3, 900, []byte("back"))
	appendFramedRTP(t, &buf, 60_000_000, 4, 2000, []byte("ok2"))

	var errs mederr.List
	st, err := ReadRTPStream(&buf, 48000, "rtp:test", &errs)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Packets) != 2 {
		t.Fatalf("expected two good packets, got %d", len(st.Packets))
	}
	if st.Packets[1].Extended != 2000 || st.Packets[1].RawTS != 2000 {
		t.Fatalf("recovery did not preserve unwrapper state: %+v", st.Packets[1])
	}
	if errs.Len() != 2 || errs.HasFatal() {
		t.Fatalf("expected two recoverable errors, got %+v", errs.Errs)
	}
	cats := map[mederr.Category]bool{}
	for _, e := range errs.Errs {
		if e.Track != "rtp:test" || !e.Recoverable {
			t.Fatalf("error not located/recoverable: %+v", e)
		}
		cats[e.Category] = true
	}
	if !cats[mederr.CatParse] || !cats[mederr.CatTimeline] {
		t.Fatalf("missing parse/timeline categories: %+v", errs.Errs)
	}
}
