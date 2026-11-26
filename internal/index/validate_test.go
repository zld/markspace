package index

import (
	"math"
	"testing"

	"mediarc/internal/model"

	"github.com/pion/rtp"
)

func rtpBytes(ts uint32) []byte {
	pkt := rtp.Packet{Header: rtp.Header{Version: 2, Timestamp: ts, SSRC: 7}, Payload: []byte{1}}
	b, err := pkt.Marshal()
	if err != nil {
		panic(err)
	}
	return b
}

func TestValidate(t *testing.T) {
	bounds := map[int64]bool{0: true, 36: true}
	recs := []Record{
		{Codec: "h264", Track: 1, Offset: 0, Length: 36, PTS: 0, Timescale: 90000, RTP: rtpBytes(100)},
		{Codec: "h264", Track: 1, Offset: 50, Length: 10, PTS: 1, Timescale: 90000},  // misaligned
		{Codec: "h264", Track: 1, Offset: 36, Length: 100, PTS: 2, Timescale: 90000}, // out of range
		{Codec: "h264", Track: 1, Offset: math.MaxInt64 - 1, Length: 8},              // overflow
		{Codec: "h264", Track: 1, Offset: 36, Length: 5, RTP: []byte{0xff}},          // bad RTP
	}
	ok, errs := Validate(recs, 72, bounds)
	if len(ok) != 1 {
		t.Fatalf("got %d valid records, want 1", len(ok))
	}
	if !ok[0].HasRTP || ok[0].RTPTimestamp != 100 {
		t.Fatalf("RTP not parsed: %+v", ok[0])
	}
	if len(errs) != 4 {
		t.Fatalf("got %d errors, want 4: %v", len(errs), errs)
	}
	want := []struct {
		code model.Code
		off  int64
	}{
		{model.ErrMisaligned, 50},
		{model.ErrOutOfRange, 36},
		{model.ErrOverflow, math.MaxInt64 - 1},
		{model.ErrRTP, 36},
	}
	for i, w := range want {
		if errs[i].Code != w.code || errs[i].Offset != w.off {
			t.Fatalf("err[%d] = %v@%d, want %v@%d", i, errs[i].Code, errs[i].Offset, w.code, w.off)
		}
	}
}

func TestValidateNoBoundaries(t *testing.T) {
	recs := []Record{{Track: 2, Offset: 3, Length: 4}}
	ok, errs := Validate(recs, 10, nil)
	if len(errs) != 0 || len(ok) != 1 {
		t.Fatalf("got ok=%d errs=%d, want 1/0", len(ok), len(errs))
	}
}
