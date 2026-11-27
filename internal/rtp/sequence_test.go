package rtp_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"

	"mediasync/internal/model"
	rtpx "mediasync/internal/rtp"
)

func mkPacket(ssrc uint32, seq uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
		},
		Payload: []byte{0x01, 0x02, 0x03},
	}
}

func ms(n int) model.Ticks { return model.Ticks(n) * model.Milli }

func feed(t *testing.T, r *rtpx.Reconstructor, pkt *rtp.Packet, at model.Ticks) rtpx.Outcome {
	t.Helper()
	return r.Handle(pkt, at)
}

func TestInOrderCommit(t *testing.T) {
	r, err := rtpx.NewReconstructor(111, 4, 90000)
	if err != nil {
		t.Fatal(err)
	}
	var committed int
	for seq := uint16(0); seq < 5; seq++ {
		out := feed(t, r, mkPacket(111, seq, uint32(seq)*3000), ms(int(seq)))
		if out.Status != rtpx.StatusCommitted || len(out.Committed) != 1 {
			t.Fatalf("seq %d: want immediate commit, got %s/%d", seq, out.Status, len(out.Committed))
		}
		committed += len(out.Committed)
	}
	st := r.Snapshot(committed)
	if st.Missing != 0 || st.Duplicates != 0 || st.Late != 0 {
		t.Fatalf("clean stream must have no anomalies: %+v", st)
	}
}

func TestReorderWithinWindowRecovers(t *testing.T) {
	r, _ := rtpx.NewReconstructor(222, 8, 90000)
	// 0,1,3,2 : packet 2 delayed but arrives inside the window.
	feed(t, r, mkPacket(222, 0, 0), ms(0))
	feed(t, r, mkPacket(222, 1, 3000), ms(1))
	out3 := feed(t, r, mkPacket(222, 3, 9000), ms(3))
	if len(out3.GapExts) != 0 || len(out3.Committed) != 0 {
		t.Fatalf("gap must stay open inside window: %+v", out3)
	}
	out2 := feed(t, r, mkPacket(222, 2, 6000), ms(4))
	if len(out2.Committed) != 2 { // 0,1 already emitted; now 2,3 release together
		t.Fatalf("reorder should release 2..3, got %d", len(out2.Committed))
	}
	if st := r.Snapshot(4); st.Missing != 0 {
		t.Fatalf("recovered reorder must not count loss: %+v", st)
	}
}

func TestBurstLossOutsideWindow(t *testing.T) {
	r, _ := rtpx.NewReconstructor(333, 4, 90000)
	// Packets 1 and 2 are lost; push 0..6 so the window exhausts.
	var committed int
	var declaredGaps []uint64
	for seq := 0; seq <= 6; seq++ {
		if seq == 1 || seq == 2 {
			continue
		}
		out := feed(t, r, mkPacket(333, uint16(seq), uint32(seq)*3000), ms(seq))
		committed += len(out.Committed)
		declaredGaps = append(declaredGaps, out.GapExts...)
	}
	if len(declaredGaps) != 2 {
		t.Fatalf("want 2 declared gaps, got %v", declaredGaps)
	}
	// The lost packets arrive late now and must not resurrect committed state.
	late := feed(t, r, mkPacket(333, 1, 3000), ms(99))
	if late.Status != rtpx.StatusLate {
		t.Fatalf("post-loss arrival must be classified late, got %s", late.Status)
	}
	st := r.Snapshot(committed)
	if st.Missing != 2 || st.Late != 1 {
		t.Fatalf("want missing=2 late=1, got %+v", st)
	}
}

func TestDuplicates(t *testing.T) {
	r, _ := rtpx.NewReconstructor(444, 4, 90000)
	feed(t, r, mkPacket(444, 0, 0), ms(0))
	feed(t, r, mkPacket(444, 1, 3000), ms(1))
	dup := feed(t, r, mkPacket(444, 0, 0), ms(2))
	if dup.Status != rtpx.StatusDuplicate {
		t.Fatalf("retransmission of seen seq must be duplicate, got %s", dup.Status)
	}
	feed(t, r, mkPacket(444, 2, 6000), ms(3))
	if st := r.Snapshot(3); st.Duplicates != 1 || st.Committed != 3 {
		t.Fatalf("duplicate must not change commit count: %+v", st)
	}
}

func TestSequenceWrap(t *testing.T) {
	r, _ := rtpx.NewReconstructor(555, 4, 90000)
	// Approach the 16-bit boundary and cross it.
	seqs := []uint16{0xFFFD, 0xFFFE, 0xFFFF, 0x0000, 0x0001}
	var committed int
	for i, seq := range seqs {
		out := feed(t, r, mkPacket(555, seq, uint32(i)*3000), ms(i))
		committed += len(out.Committed)
		for _, p := range out.Committed {
			if p.ExtSequence < (1<<16) && p.Sequence <= 0xFFFD {
				continue
			}
			if p.Sequence < 3 && p.ExtSequence < (1<<16) {
				t.Fatalf("wrapped seq %d not extended past 65535: ext=%d", p.Sequence, p.ExtSequence)
			}
		}
	}
	if committed != 5 {
		t.Fatalf("all 5 wrap packets should commit, got %d", committed)
	}
	if st := r.Snapshot(committed); st.Missing != 0 {
		t.Fatalf("wrap must not manufacture losses: %+v", st)
	}
}

func TestFlushDeclaresRemainingGaps(t *testing.T) {
	r, _ := rtpx.NewReconstructor(666, 64, 90000)
	// Wide window: 0,1 then 4 — gaps never exhaust during the stream.
	feed(t, r, mkPacket(666, 0, 0), ms(0))
	feed(t, r, mkPacket(666, 1, 3000), ms(1))
	feed(t, r, mkPacket(666, 4, 12000), ms(4))
	out := r.Flush()
	if len(out.GapExts) != 2 || len(out.Committed) != 1 {
		t.Fatalf("flush must declare 2 gaps and commit seq 4, got %+v", out)
	}
}

func TestWindowLimitRejected(t *testing.T) {
	if _, err := rtpx.NewReconstructor(1, 0, 90000); model.AsMediaError(err) == nil {
		t.Fatal("zero window must be rejected")
	}
	if _, err := rtpx.NewReconstructor(1, model.MaxReorderWindow+1, 90000); model.AsMediaError(err) == nil {
		t.Fatal("over-ceiling window must be rejected")
	}
	if _, err := rtpx.NewReconstructor(1, 4, 0); model.AsMediaError(err) == nil {
		t.Fatal("zero clock rate must be rejected")
	}
}

func TestEventLogRoundTripAndBadLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	var datagrams [][]byte
	var arrivals []model.Ticks
	for seq := uint16(0); seq < 4; seq++ {
		raw, err := mkPacket(777, seq, uint32(seq)*3000).Marshal()
		if err != nil {
			t.Fatal(err)
		}
		datagrams = append(datagrams, raw)
		arrivals = append(arrivals, ms(int(seq)))
	}
	if err := rtpx.WriteEventLog(path, arrivals, datagrams); err != nil {
		t.Fatal(err)
	}
	src, err := rtpx.OpenEventLog(path, model.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	r, _ := rtpx.NewReconstructor(777, 4, 90000)
	var committed int
	for {
		_, pkt, nerr := src.Next()
		if errors.Is(nerr, io.EOF) {
			break
		}
		if nerr != nil {
			t.Fatalf("unexpected: %v", nerr)
		}
		if pkt == nil {
			break
		}
		out := r.Handle(pkt, ms(committed))
		committed += len(out.Committed)
	}
	if committed != 4 {
		t.Fatalf("want 4 committed from log, got %d", committed)
	}
}

func TestEventLogBadLineLocated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.jsonl")
	good, err := mkPacket(888, 0, 0).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	rec := rtpx.EventRecord{Index: 1, ArrivalNS: 0, Datagram: base64.StdEncoding.EncodeToString(good)}
	line1, _ := json.Marshal(&rec)
	content := append(line1, '\n')
	content = append(content, []byte("{not-json}\n")...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := rtpx.OpenEventLog(path, model.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, pkt, nerr := src.Next(); nerr != nil || pkt == nil {
		t.Fatalf("first valid line must parse: %v", nerr)
	}
	_, _, nerr := src.Next()
	me := model.AsMediaError(nerr)
	if me == nil || me.Class != model.ClassRTP || me.Offset < 0 {
		t.Fatalf("bad line must produce offset-bearing rtp_parse error, got %v", nerr)
	}
}
