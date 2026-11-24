package ts

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"mediad/internal/clock"
)

// rawPacket builds a minimal packet with the given PID, PUSI, CC and payload.
func rawPacket(pid uint16, pusi bool, cc uint8, payload []byte) []byte {
	b := make([]byte, PacketSize)
	b[0] = SyncByte
	if pusi {
		b[1] |= 0x40
	}
	b[1] |= byte(pid >> 8)
	b[2] = byte(pid)
	if len(payload) > 0 {
		b[3] = 0x10 | cc // adaptation_control=01: payload only
		copy(b[4:], payload)
	} else {
		b[3] = 0x10 | cc // payload only, zero-length payload
	}
	return b
}

// pcrPacket embeds an adaptation field carrying pcrBase (90 kHz ticks).
func pcrPacket(pid uint16, cc uint8, pcrBase int64) []byte {
	b := rawPacket(pid, false, cc, nil)
	b[3] = 0x20 | cc // adaptation_control=10: adaptation field only
	b[4] = 7         // adaptation field length: 7 bytes follow
	b[5] = 0x10      // PCR flag
	v := uint64(pcrBase)
	b[6] = byte(v >> 25)
	b[7] = byte(v >> 17)
	b[8] = byte(v >> 9)
	b[9] = byte(v >> 1)
	b[10] = byte(v<<7) & 0xfe
	b[11] = 0
	return b
}

func TestParseHeaderAndPCR(t *testing.T) {
	pkt, err := Parse(rawPacket(0x100, true, 5, []byte{1, 2, 3}), 376)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.PID != 0x100 || !pkt.PUSI || pkt.CC != 5 || pkt.Offset != 376 {
		t.Fatalf("header decode wrong: %+v", pkt)
	}
	if pkt.Adaptation != 1 || !pkt.HasPayload() || pkt.HasAdaptation() {
		t.Fatalf("adaptation control = %d, want payload-only", pkt.Adaptation)
	}
	if len(pkt.Payload) != PacketSize-4 || pkt.Payload[0] != 1 {
		t.Fatal("payload section not extracted correctly")
	}
	pp, err := Parse(pcrPacket(0x200, 9, 123456789), 0)
	if err != nil {
		t.Fatal(err)
	}
	base, ok := pp.PCR()
	if !ok || base != 123456789 {
		t.Fatalf("PCR = %d ok=%v, want 123456789", base, ok)
	}
}

func TestParseSyncAndAdaptationErrors(t *testing.T) {
	var re clock.RangeError
	bad := rawPacket(0, false, 0, nil)
	bad[0] = 0x00
	_, err := Parse(bad, 0)
	if !errors.As(err, &re) || re.Kind != clock.KindSync {
		t.Fatalf("want sync RangeError, got %v", err)
	}
	over := rawPacket(0, false, 0, nil)
	over[3] = 0x20 // adaptation_control=10: adaptation only
	over[4] = 200  // impossible length
	if _, err = Parse(over, 0); err == nil || !strings.Contains(err.Error(), "overrun") {
		t.Fatalf("want adaptation overrun error, got %v", err)
	}
}

func TestScanHappyPath(t *testing.T) {
	stream := bytes.NewReader(append(append(rawPacket(0x100, true, 0, nil),
		rawPacket(0x100, false, 1, nil)...), pcrPacket(0x200, 0, 42)...))
	var offsets []int64
	var pcr []int64
	err := Scan(stream, ScanOptions{}, func(e Event) error {
		if e.SyncError != nil {
			t.Fatal("unexpected sync error on clean stream")
		}
		offsets = append(offsets, e.Pkt.Offset)
		if v, ok := e.Pkt.PCR(); ok {
			pcr = append(pcr, v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 3 || offsets[1] != PacketSize || offsets[2] != 2*PacketSize {
		t.Fatalf("offsets = %v", offsets)
	}
	if len(pcr) != 1 || pcr[0] != 42 {
		t.Fatalf("pcr = %v", pcr)
	}
}

func TestScanResyncAndTruncation(t *testing.T) {
	garbage := []byte{0x01, 0x02, 0x03}
	stream := bytes.NewReader(append(append(garbage, rawPacket(0x100, true, 0, nil)...),
		0x47, 0xAB)) // partial trailing packet
	var syncs []clock.RangeError
	var pkts int
	err := Scan(stream, ScanOptions{}, func(e Event) error {
		if e.SyncError != nil {
			syncs = append(syncs, *e.SyncError)
		} else {
			pkts++
		}
		return nil
	})
	if pkts != 1 {
		t.Fatalf("want one recovered packet, got %d", pkts)
	}
	if len(syncs) != 1 || syncs[0].Start != 0 || syncs[0].End != 3 {
		t.Fatalf("sync error = %+v", syncs)
	}
	var re clock.RangeError
	if !errors.As(err, &re) || re.Kind != clock.KindTruncated {
		t.Fatalf("want truncated RangeError, got %v", err)
	}
	// Truncation offset points at the start of the incomplete packet.
	if re.Start != int64(3+PacketSize) {
		t.Fatalf("truncation offset = %d, want %d", re.Start, 3+PacketSize)
	}
}

func TestScanSizeLimit(t *testing.T) {
	stream := bytes.NewReader(bytes.Repeat([]byte{SyncByte}, PacketSize*4))
	err := Scan(stream, ScanOptions{MaxSize: PacketSize * 2}, func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("want size-limit error, got %v", err)
	}
}
