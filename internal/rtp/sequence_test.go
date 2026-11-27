package rtp

import (
	"testing"

	"github.com/example/mediad/internal/model"
	"github.com/pion/rtp"
)

func datagram(t *testing.T, seq uint16, ts uint32, payload []byte, offset int64) struct {
	Data   []byte
	Offset int64
} {
	t.Helper()
	pkt := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, Timestamp: ts, PayloadType: 96, Version: 2},
		Payload: payload,
	}
	b, err := pkt.Marshal()
	if err != nil {
		t.Fatalf("marshal seq=%d: %v", seq, err)
	}
	return struct {
		Data   []byte
		Offset int64
	}{Data: b, Offset: offset}
}

func TestOrderClean(t *testing.T) {
	var in []struct {
		Data   []byte
		Offset int64
	}
	for seq := uint16(10); seq < 15; seq++ {
		in = append(in, datagram(t, seq, uint32(seq)*960, []byte{byte(seq)}, int64(seq)*100))
	}
	res, err := Order(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.LostCount != 0 || res.DupCount != 0 || res.Isolated {
		t.Fatalf("unexpected anomalies: %+v", res)
	}
	if len(res.Packets) != 5 || res.Packets[0].Sequence != 10 {
		t.Fatalf("accepted packets wrong: %+v", res.Packets)
	}
}

func TestOrderLossAndRanges(t *testing.T) {
	in := []struct {
		Data   []byte
		Offset int64
	}{
		datagram(t, 1, 0, []byte{1}, 0),
		datagram(t, 2, 1, []byte{2}, 10),
		// 3 and 4 missing
		datagram(t, 5, 4, []byte{5}, 20),
		// 6 missing
		datagram(t, 7, 6, []byte{7}, 30),
	}
	res, err := Order(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.LostCount != 3 {
		t.Fatalf("lost = %d, want 3", res.LostCount)
	}
	// Extended seqs anchor the first packet (seq 1) at zero.
	wantRanges := []Range{{From: 2, To: 3}, {From: 5, To: 5}}
	if len(res.LossRanges) != len(wantRanges) {
		t.Fatalf("ranges = %+v, want %+v", res.LossRanges, wantRanges)
	}
	for i, r := range wantRanges {
		if res.LossRanges[i] != r {
			t.Fatalf("range %d = %+v, want %+v", i, res.LossRanges[i], r)
		}
	}
}

func TestOrderDuplicateAndConflict(t *testing.T) {
	in := []struct {
		Data   []byte
		Offset int64
	}{
		datagram(t, 1, 100, []byte{0xAA, 0xBB}, 0),
		datagram(t, 2, 200, []byte{0xCC}, 10),
		datagram(t, 2, 200, []byte{0xCC}, 20), // identical duplicate
		datagram(t, 3, 300, []byte{0xDD}, 30),
		datagram(t, 3, 300, []byte{0xEE}, 40), // same seq, different payload
	}
	res, err := Order(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.DupCount != 1 {
		t.Fatalf("duplicates = %d, want 1", res.DupCount)
	}
	if len(res.Conflicts) != 1 || res.ConflictSeq[0] != 3 {
		t.Fatalf("conflicts = %+v", res.Conflicts)
	}
	if !res.Isolated {
		t.Fatal("conflicted material must be isolated")
	}
	if len(res.Packets) != 3 { // one entry per distinct sequence
		t.Fatalf("accepted = %d, want 3", len(res.Packets))
	}
	// Duplicate must not advance the clock: 3 accepted packets only.
	if res.Packets[1].Offset != 10 {
		t.Fatalf("duplicate should keep first reception, got offset %d", res.Packets[1].Offset)
	}
}

func TestOrderOutOfOrderAndWrap(t *testing.T) {
	in := []struct {
		Data   []byte
		Offset int64
	}{
		datagram(t, 65534, 0, []byte{1}, 0),
		datagram(t, 0, 2, []byte{3}, 20),  // arrives before 65535
		datagram(t, 65535, 1, []byte{2}, 10),
		datagram(t, 1, 3, []byte{4}, 30),
	}
	res, err := Order(in)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]uint16, len(res.Packets))
	for i, p := range res.Packets {
		got[i] = p.Sequence
	}
	want := []uint16{65534, 65535, 0, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	if res.LostCount != 0 {
		t.Fatalf("wrap produced false loss: %+v", res.Lost)
	}
	// Extended sequences stay monotonic across the 16-bit wrap.
	wantExt := []uint64{0, 1, 2, 3}
	for i := range wantExt {
		if res.Packets[i].ExtSeq != wantExt[i] {
			t.Fatalf("extSeq = %v, want %v", extSeqs(res.Packets), wantExt)
		}
	}
}

func extSeqs(pkts []model.Packet) []uint64 {
	out := make([]uint64, len(pkts))
	for i, p := range pkts {
		out[i] = p.ExtSeq
	}
	return out
}

func TestOrderBadDatagram(t *testing.T) {
	_, err := Order([]struct {
		Data   []byte
		Offset int64
	}{{Data: []byte{0x00}, Offset: 7}})
	if err == nil {
		t.Fatal("expected error for malformed RTP")
	}
	me := err.(*model.Error)
	if me.Code != model.CodeIllegalPacket || me.Stage != model.StageRTP || me.Offset != 7 {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPayloadDigestStable(t *testing.T) {
	pkts := []model.Packet{
		{ExtSeq: 1, Payload: []byte{1}},
		{ExtSeq: 2, Payload: []byte{2, 3}},
	}
	d1 := PayloadDigest(pkts)
	d2 := PayloadDigest(pkts)
	if d1 != d2 {
		t.Fatal("digest must be deterministic")
	}
	pkts[1].Payload[0] = 9
	if PayloadDigest(pkts) == d1 {
		t.Fatal("digest must change with content")
	}
}
