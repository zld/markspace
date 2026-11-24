package ts

import (
	"errors"
	"testing"

	"mediarun/internal/clock"
)

func makeRaw(pid uint16, cc uint8, afc uint8, payload []byte) []byte {
	b := make([]byte, PacketSize)
	b[0] = SyncByte
	b[1] = byte(pid >> 8) // TEI/priority clear, no PUSI
	b[2] = byte(pid)
	b[3] = (afc << 4) | (cc & 0x0f)
	copy(b[4:], payload)
	return b
}

func TestParsePacket_BasicAndPCR(t *testing.T) {
	raw := makeRaw(0x100, 7, 0x03, nil)
	// adaptation field: length 7, PCR flag set, PCR_base=0x123456789
	raw[4] = 7
	raw[5] = 0x10
	base := uint64(0x123456789)
	raw[6] = byte(base >> 25)
	raw[7] = byte(base >> 17)
	raw[8] = byte(base >> 9)
	raw[9] = byte(base >> 1)
	raw[10] = byte(base<<7) | 0x7e // extension high bit reserved-ish
	raw[11] = 0
	p, err := ParsePacket(raw, 3, 564)
	if err != nil {
		t.Fatal(err)
	}
	if p.PID != 0x100 || p.CC != 7 || !p.HasAdaptation || !p.HasPayload {
		t.Fatalf("header parse wrong: %+v", p)
	}
	if !p.Adaptation.HasPCR || p.Adaptation.PCRBase != base {
		t.Fatalf("PCR parse: got base=%d want %d", p.Adaptation.PCRBase, base)
	}
	if p.Index != 3 || p.Offset != 564 {
		t.Fatalf("position tracking wrong: %+v", p)
	}
}

func TestParsePacket_Errors(t *testing.T) {
	if _, err := ParsePacket(make([]byte, 10), 0, 0); err == nil {
		t.Fatal("short buffer must fail")
	}
	bad := makeRaw(0x100, 0, 0x01, nil)
	bad[0] = 0x00
	if _, err := ParsePacket(bad, 0, 0); !errors.Is(err, ErrSyncByte) {
		t.Fatalf("bad sync byte must fail with ErrSyncByte, got %v", err)
	}
	oversized := makeRaw(0x100, 0, 0x02, nil)
	oversized[4] = 200
	if _, err := ParsePacket(oversized, 0, 0); err == nil {
		t.Fatal("oversized adaptation field must fail")
	}
}

func TestSplitStream_ResyncAndTruncation(t *testing.T) {
	good1 := makeRaw(0x100, 0, 0x01, []byte{1, 2, 3})
	corrupt := makeRaw(0x101, 0, 0x01, nil)
	corrupt[0] = 0xFF
	good2 := makeRaw(0x102, 1, 0x01, nil)
	data := append(append(append([]byte{}, good1...), corrupt...), good2...)
	data = append(data, 0x47, 0x01) // trailing partial

	rec := &clock.Recorder{}
	pkts := SplitStream(data, rec)
	if len(pkts) != 2 {
		t.Fatalf("recovered %d packets, want 2", len(pkts))
	}
	counts := rec.CountByKind()
	if counts["sync_error"] != 1 || counts["truncated"] != 1 {
		t.Fatalf("event counts = %v", counts)
	}
	ev := rec.Events()
	if ev[0].Range.Start != 188 || ev[0].Range.End != 376 {
		t.Fatalf("sync error range = %+v", ev[0].Range)
	}
	if ev[1].Range.End != int64(len(data)) {
		t.Fatalf("truncation range = %+v", ev[1].Range)
	}
}
