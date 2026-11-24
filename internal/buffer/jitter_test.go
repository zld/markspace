package buffer

import (
	"path/filepath"
	"testing"

	"github.com/pion/rtp"

	"mediarc/internal/ingest"
	"mediarc/internal/model"
)

func TestJitterBufferCountsLateDuplicateAndRestoresOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.mrtp")
	fx, err := ingest.GenerateRTPFile(ingest.RTPGenOptions{
		Path: path, Seed: 17, Packets: 48, Late: true, Duplicate: true, OutOfOrder: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	faults := &model.Collector{}
	stored, err := ingest.ReadRTPFile(path, faults)
	if err != nil {
		t.Fatal(err)
	}
	jb := New(32, 8, 48000, faults)
	for _, p := range stored {
		jb.Push(p)
	}
	items, gaps := jb.Flush()
	st := jb.Stats()
	if st.Duplicate != 1 {
		t.Fatalf("duplicates=%d want 1", st.Duplicate)
	}
	if st.Late == 0 {
		t.Fatalf("late=%d want >0", st.Late)
	}
	if len(gaps) != 0 || st.Lost != 0 {
		t.Fatalf("gaps=%+v lost=%d; generated stream should be complete", gaps, st.Lost)
	}
	if len(items) != fx.CanonicalPackets {
		t.Fatalf("unique=%d want %d", len(items), fx.CanonicalPackets)
	}
	for i := 1; i < len(items); i++ {
		if items[i-1].ExtSequence >= items[i].ExtSequence {
			t.Fatalf("items not sorted at %d", i)
		}
	}
}

func TestJitterBufferWindowCloseCountsLoss(t *testing.T) {
	faults := &model.Collector{}
	jb := New(8, 2, 48000, faults)
	for _, seq := range []uint16{10, 11, 14, 15} {
		jb.Push(ingest.StoredPacket{Packet: testPacket(seq)})
	}
	items, gaps := jb.Flush()
	if len(items) != 4 {
		t.Fatalf("items=%d", len(items))
	}
	st := jb.Stats()
	if st.Lost != 2 || len(gaps) != 1 || gaps[0].Count != 2 {
		t.Fatalf("lost=%d gaps=%+v", st.Lost, gaps)
	}
	if !faults.Has(model.FaultRTPLoss) {
		t.Fatal("loss did not enter unified fault model")
	}
}

func testPacket(seq uint16) rtp.Packet {
	return rtp.Packet{Header: rtp.Header{SequenceNumber: seq, Timestamp: uint32(seq) * 480}}
}
