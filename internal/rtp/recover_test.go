package rtp

import (
	"testing"

	"github.com/pion/rtp"
)

func pkt(seq uint16, fill byte) *rtp.Packet {
	p := make([]byte, 16)
	for i := range p {
		p[i] = fill
	}
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 160, SSRC: 7},
		Payload: p,
	}
}

func TestInWindowRecovery(t *testing.T) {
	r := NewRecoverer(Config{Window: 8})
	var delivered [][]byte
	push := func(p *rtp.Packet) {
		out, err := r.Push(p)
		if err != nil {
			t.Fatal(err)
		}
		delivered = append(delivered, out...)
	}
	push(pkt(0, 0xAA))
	// 缺口 1..3（窗内）→ 恢复
	push(pkt(4, 0xBB))
	st := r.Stats()
	if st.Recovered != 3 || st.Unrecovered != 0 {
		t.Fatalf("stats %+v", st)
	}
	if len(delivered) != 5 {
		t.Fatalf("delivered=%d, want 5", len(delivered))
	}
	// 恢复包为上一载荷的确定性填充
	for _, d := range delivered[1:4] {
		if d[0] != 0xAA {
			t.Fatalf("fill payload=%x, want aa", d[0])
		}
	}
	if st.RecoveryRate != 1.0 {
		t.Fatalf("rate=%f", st.RecoveryRate)
	}
}

func TestOutOfWindowUnrecovered(t *testing.T) {
	r := NewRecoverer(Config{Window: 4})
	r.Push(pkt(0, 0x01))
	out, _ := r.Push(pkt(10, 0x02)) // 缺口 9 > 窗口 4
	st := r.Stats()
	if st.Unrecovered != 9 || st.Recovered != 0 {
		t.Fatalf("stats %+v", st)
	}
	if len(out) != 1 || out[0][0] != 0x02 {
		t.Fatalf("out-of-window must not fabricate: %d packets", len(out))
	}
	want := float64(2) / 11
	if st.RecoveryRate < want-1e-9 || st.RecoveryRate > want+1e-9 {
		t.Fatalf("rate=%f, want %f", st.RecoveryRate, want)
	}
}

func TestDuplicateDropped(t *testing.T) {
	r := NewRecoverer(Config{Window: 8})
	r.Push(pkt(5, 0x01))
	out, _ := r.Push(pkt(5, 0x01)) // 重复
	if len(out) != 0 {
		t.Fatal("duplicate must be dropped")
	}
	if st := r.Stats(); st.Received != 1 {
		t.Fatalf("received=%d", st.Received)
	}
}

func TestUnmarshalRoundtrip(t *testing.T) {
	raw, err := pkt(42, 0x5A).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	p, err := UnmarshalPacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.SequenceNumber != 42 || p.Payload[0] != 0x5A {
		t.Fatalf("roundtrip: seq=%d", p.SequenceNumber)
	}
}
