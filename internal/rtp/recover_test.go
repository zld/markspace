package rtp

import (
	"testing"

	"github.com/pion/rtp"
)

func TestReorderDedupeAndRecoverSingleLoss(t *testing.T) {
	seqs := []uint16{10, 11, 12}
	p0 := NewMediaPacket(10, 1000, []byte("aaaa"))
	p1 := NewMediaPacket(11, 2000, []byte("bbbb"))
	p2 := NewMediaPacket(12, 3000, []byte("cccc"))
	fec, err := BuildFECPacket(1, 100, 1, seqs, [][]byte{p0.Payload, p1.Payload, p2.Payload})
	if err != nil {
		t.Fatal(err)
	}

	lost := NewMediaPacket(21, 0, []byte("dddd"))
	group := []uint16{20, 21, 22}
	a := NewMediaPacket(20, 0, []byte("AAAA"))
	c := NewMediaPacket(22, 0, []byte("CCCC"))
	parity, err := BuildFECPacket(1, 101, 2, group, [][]byte{a.Payload, lost.Payload, c.Payload})
	if err != nil {
		t.Fatal(err)
	}

	packets := []*rtp.Packet{p2, p1, p0, p0, fec, a, c, parity}
	report := RecoverPackets(packets)
	if report.Duplicates != 1 {
		t.Fatalf("duplicates=%d", report.Duplicates)
	}
	if report.OutOfOrder == 0 {
		t.Fatal("expected out-of-order arrivals")
	}
	if report.Recovered != 1 || report.Lost != 1 || report.Unrecoverable != 0 {
		t.Fatalf("recovery=%+v", report)
	}
	if report.RecoveryRate != 100 || len(report.Ordered) != 6 {
		t.Fatalf("rate=%v ordered=%d", report.RecoveryRate, len(report.Ordered))
	}
	if string(report.Ordered[3].Payload) != "AAAA" || string(report.Ordered[4].Payload) != "dddd" {
		t.Fatalf("order/recovery wrong: %q %q", report.Ordered[3].Payload, report.Ordered[4].Payload)
	}
}

func TestMultipleLossIsGap(t *testing.T) {
	group := []uint16{30, 31, 32}
	a := NewMediaPacket(30, 0, []byte("aaa"))
	lost1 := NewMediaPacket(31, 0, []byte("bbb"))
	lost2 := NewMediaPacket(32, 0, []byte("ccc"))
	fec, err := BuildFECPacket(1, 130, 3, group, [][]byte{a.Payload, lost1.Payload, lost2.Payload})
	if err != nil {
		t.Fatal(err)
	}
	r := RecoverPackets([]*rtp.Packet{a, fec})
	if r.Recovered != 0 || r.Unrecoverable != 2 || len(r.Gaps) != 1 {
		t.Fatalf("report=%+v gaps=%+v", r, r.Gaps)
	}
}
