package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"mediacheck/internal/gap"
)

func pkts(seqs ...uint16) []*rtp.Packet {
	out := make([]*rtp.Packet, 0, len(seqs))
	for _, s := range seqs {
		out = append(out, &rtp.Packet{Header: rtp.Header{SequenceNumber: s}, Payload: []byte{0}})
	}
	return out
}

func seqList(ps []TimedPacket) []uint16 {
	out := make([]uint16, len(ps))
	for i, p := range ps {
		out[i] = p.Packet.SequenceNumber
	}
	return out
}

func TestWrapAroundReleasesMonotonicSamples(t *testing.T) {
	r, err := NewReorderer(Config{Track: "a", SampleRate: 48000, StartSequence: 65534, Window: 8, SamplesPerPacket: 100})
	if err != nil {
		t.Fatal(err)
	}
	var got []TimedPacket
	for _, p := range pkts(65534, 65535, 0, 1) {
		res, err := r.Push(p)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, res.Ready...)
	}
	rest, err := r.Flush()
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, rest...)
	if want := []uint16{65534, 65535, 0, 1}; len(got) != 4 {
		t.Fatalf("seq=%v want %v", seqList(got), want)
	}
	for i, p := range got {
		if p.StartSample != int64(i)*100 {
			t.Fatalf("packet %d start=%d want %d", i, p.StartSample, i*100)
		}
	}
}

func TestReorderAndDeduplicate(t *testing.T) {
	r, _ := NewReorderer(Config{Track: "a", SampleRate: 48000, StartSequence: 0, Window: 16, SamplesPerPacket: 64})
	var got []TimedPacket
	for i, p := range pkts(0, 2, 2, 1, 3) {
		res, err := r.Push(p)
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 && !res.Duplicate {
			t.Fatal("second seq=2 was not deduplicated")
		}
		got = append(got, res.Ready...)
	}
	rest, _ := r.Flush()
	got = append(got, rest...)
	if want := []uint16{0, 1, 2, 3}; len(got) != 4 || seqList(got)[1] != 1 {
		t.Fatalf("ordered=%v want %v", seqList(got), want)
	}
}

func TestBurstLossAndLateOutsideWindow(t *testing.T) {
	ledger := gap.NewLedger("cam", 48000)
	r, _ := NewReorderer(Config{
		Track: "cam", SampleRate: 48000, StartSequence: 0,
		Window: 2, SamplesPerPacket: 64, Ledger: ledger,
	})
	// 2 and 3 are absent. High sequence 5 ages the gap past window=2.
	for _, p := range pkts(0, 1, 4, 5) {
		if _, err := r.Push(p); err != nil {
			t.Fatal(err)
		}
	}
	gaps := ledger.Gaps()
	if len(gaps) != 1 || gaps[0].Kind != gap.KindMissing || gaps[0].PacketCount != 2 {
		t.Fatalf("gaps=%+v want one two-packet missing burst", gaps)
	}
	if gaps[0].StartSample != 128 || gaps[0].EndSample != 256 {
		t.Fatalf("sample interval=[%d,%d) want [128,256)", gaps[0].StartSample, gaps[0].EndSample)
	}

	// After the frontier has moved to 6 and high is 5, seq 2 is late.
	res, err := r.Push(pkts(2)[0])
	if err != nil {
		t.Fatal(err)
	}
	if !res.Late {
		t.Fatal("seq 2 should be reported late outside the window")
	}
	kinds := map[gap.Kind]int{}
	for _, g := range ledger.Gaps() {
		kinds[g.Kind]++
	}
	if kinds[gap.KindLate] != 1 {
		t.Fatalf("kinds=%v want one late gap", kinds)
	}
}

func TestPushRawParsesPionRTP(t *testing.T) {
	src := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 7, Timestamp: 700}, Payload: []byte("pcm")}
	raw, err := src.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	r, _ := NewReorderer(Config{Track: "a", SampleRate: 48000, StartSequence: 7, Window: 4, SamplesPerPacket: 1})
	res, err := r.PushRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExtSeq != 7 || len(res.Ready) != 1 || string(res.Ready[0].Packet.Payload) != "pcm" {
		t.Fatalf("raw result=%+v err=%v", res, err)
	}
}
