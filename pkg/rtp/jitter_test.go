package rtp_test

import (
	"testing"

	"github.com/zld/medcheck/pkg/media"
	mrtp "github.com/zld/medcheck/pkg/rtp"
)

func mustPacket(t *testing.T, seq uint16, ts uint32, payload string) []byte {
	t.Helper()
	b, err := mrtp.MakeRawPacket(96, 0x12345678, seq, ts, false, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestJitterReorderDuplicateRepairAndUnrecoverableGap(t *testing.T) {
	jb, err := mrtp.NewJitterBuffer("cam0/video", 8)
	if err != nil {
		t.Fatal(err)
	}

	// 102 arrives before 101; the second 102 is a duplicate.
	for _, item := range []struct {
		seq     uint16
		payload string
	}{
		{100, "p100"}, {102, "p102"}, {102, "dup102"}, {103, "p103"},
		{104, "p104"}, {106, "p106"}, {107, "p107"},
	} {
		raw := mustPacket(t, item.seq, uint32(item.seq)*3000, item.payload)
		if _, err := jb.PushRaw(raw, int64(item.seq)*1000, int64(item.seq)*17); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := jb.ApplyRepair(mrtp.RepairPacket{
		Sequence: 101, Timestamp: 101 * 3000, Payload: []byte("repair101"), Kind: mrtp.RepairRetransmission,
	}); err != nil {
		t.Fatalf("in-window repair should be accepted: %v", err)
	}

	out := jb.Flush()
	if out.Stats.Repaired != 1 {
		t.Fatalf("recovered=%d want 1", out.Stats.Repaired)
	}
	if out.Stats.Gaps != 1 || len(out.Gaps) != 1 || out.Gaps[0].Sequence != 105 {
		t.Fatalf("gaps=%+v stats=%+v", out.Gaps, out.Stats)
	}
	if out.Stats.Duplicates != 1 {
		t.Fatalf("duplicates=%d want 1", out.Stats.Duplicates)
	}
	want := []uint16{100, 101, 102, 103, 104, 106, 107}
	if len(out.Packets) != len(want) {
		t.Fatalf("ordered packets=%d want %d", len(out.Packets), len(want))
	}
	for i, seq := range want {
		if out.Packets[i].Sequence != seq {
			t.Fatalf("packet %d seq=%d want %d", i, out.Packets[i].Sequence, seq)
		}
	}
	if !out.Packets[1].Recovered || string(out.Packets[1].RTP.Payload) != "repair101" {
		t.Fatalf("repair payload/provenance wrong: %+v", out.Packets[1])
	}

	late := mustPacket(t, 105, 105*3000, "too-late")
	ack, err := jb.PushRaw(late, 999_000, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Disposition != media.CatLateWindow || jb.Stats.Late != 1 {
		t.Fatalf("late ack=%+v stats=%+v", ack, jb.Stats)
	}
	if !jb.Issues.Has(media.CatLateWindow) || !jb.Issues.Has(media.CatUnrecoverableGap) {
		t.Fatalf("missing shared error records: %+v", jb.Issues)
	}
}

func TestExtendedSequenceWrap(t *testing.T) {
	jb, err := mrtp.NewJitterBuffer("wrap", 8)
	if err != nil {
		t.Fatal(err)
	}
	var exts []uint64
	for _, seq := range []uint16{65534, 65535, 0, 1} {
		ack, err := jb.Push(mustDecoded(t, seq))
		if err != nil {
			t.Fatal(err)
		}
		exts = append(exts, ack.ExtSeq)
	}
	want := []uint64{65534, 65535, 65536, 65537}
	for i := range want {
		if exts[i] != want[i] {
			t.Fatalf("ext[%d]=%d want %d (all=%v)", i, exts[i], want[i], exts)
		}
	}
	if jb.Stats.Wraps != 1 {
		t.Fatalf("wraps=%d want 1", jb.Stats.Wraps)
	}
}

func mustDecoded(t *testing.T, seq uint16) *mrtp.Packet {
	t.Helper()
	raw := mustPacket(t, seq, 123, "x")
	p, err := mrtp.ParsePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &mrtp.Packet{RTP: p}
}
