package ts

import (
	"bytes"
	"testing"

	"medkernel/media"
)

func TestParseBasicPacketsAndPCR(t *testing.T) {
	var stream bytes.Buffer
	// 6 packets on PID 0x100; packets 2 and 4 carry a forward PCR, the
	// first packet is payload-only.
	for i := 0; i < 6; i++ {
		var pcr int64 = -1
		if i >= 2 && i%2 == 0 {
			pcr = int64(i) * 27_000_000 // 1 s spacing at 27 MHz
		}
		stream.Write(BuildPacket(0x100, uint8(i), i == 0, []byte{0xAA, 0xBB}, pcr, false))
	}
	p := NewParser(Size188)
	var got []Packet
	err := p.Parse(&stream, func(pk Packet) error { got = append(got, pk); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if p.Stats.Total != 6 || len(got) != 6 {
		t.Fatalf("total=%d got=%d", p.Stats.Total, len(got))
	}
	if p.Stats.PCRPresent != 2 || p.Stats.BytesRead != 6*188 {
		t.Fatalf("pcr=%d bytes=%d", p.Stats.PCRPresent, p.Stats.BytesRead)
	}
	// Packet 0 has payload only; packet 2 carries the first PCR (2s).
	if !got[0].HasPayload || got[0].HasAdaptation {
		t.Fatalf("first packet flags wrong: adapt=%v payload=%v",
			got[0].HasAdaptation, got[0].HasPayload)
	}
	if got[2].PCR27 != 2*27_000_000 {
		t.Fatalf("PCR decode: %d want %d", got[2].PCR27, 2*27_000_000)
	}
}

func TestParse204AndTruncationAndSync(t *testing.T) {
	pad204 := func(pk []byte) []byte {
		big := make([]byte, 204)
		copy(big, pk)
		// 16 trailing FEC bytes stay zero; the detector sees the next
		// packet's sync byte at offset 188 and locks onto size 204.
		return big
	}
	var stream bytes.Buffer
	for i := 0; i < 3; i++ {
		stream.Write(pad204(BuildPacket(0x200, uint8(i), i == 0, []byte{1}, -1, false)))
	}
	// Corrupt the sync byte of the fourth packet; the parser must skip the
	// dead packet and resynchronize on the intact packet behind it.
	raw := pad204(BuildPacket(0x200, 3, false, []byte{2}, -1, false))
	raw[0] = 0x00
	stream.Write(raw)
	stream.Write(pad204(BuildPacket(0x200, 4, false, []byte{3}, -1, false)))
	// Truncated tail (sync byte plus 40 bytes only).
	stream.Write(BuildPacket(0x200, 5, false, nil, -1, false)[:40])

	p := NewParser(0)
	if err := p.Parse(&stream, nil); err != nil {
		t.Fatal(err)
	}
	if p.Stats.PacketSize != Size204 {
		t.Fatalf("auto-detected size=%d want 204", p.Stats.PacketSize)
	}
	if p.Stats.Total != 4 {
		t.Fatalf("recovered total packets=%d want 4", p.Stats.Total)
	}
	if p.Stats.SyncErrors != 1 || p.Stats.Truncated != 1 {
		t.Fatalf("sync=%d trunc=%d", p.Stats.SyncErrors, p.Stats.Truncated)
	}
	f := p.Faults[0]
	if f.Class != media.ClassSync || f.ByteOff != 3*204 {
		t.Fatalf("sync fault details wrong: %+v", f)
	}
}

func TestPESPTSAndTrackers(t *testing.T) {
	var stream bytes.Buffer
	near := uint64(media.Wrap33 - 2)
	stream.Write(BuildPacket(0x110, 0, true, BuildPESPayload(near, 10), -1, false))
	stream.Write(BuildPacket(0x110, 1, true, BuildPESPayload(3, 10), -1, false))
	p := NewParser(Size188)
	pts := NewPTSTracker(5)
	var ext []int64
	err := p.Parse(&stream, func(pk Packet) error {
		if pk.HasPTS {
			v, err := pts.Push(pk.PTS90, pk.PID, pk.Index)
			if err != nil {
				return err
			}
			ext = append(ext, v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ext) != 2 || ext[0] != media.Wrap33-2 || ext[1] != media.Wrap33+3 {
		t.Fatalf("PTS unwrap across 33-bit boundary failed: %v", ext)
	}
	// PCR backwards beyond tolerance must produce a ClassClock error.
	tr := NewPCRTracker(90_000, 100, 50_000)
	if _, err := tr.Push(10*27_000_000, 0x100, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Push((10*27_000_000 - 200*300), 0x100, 1); err == nil ||
		media.ClassOf(err) != media.ClassClock {
		t.Fatalf("backwards PCR not detected: %v", err)
	}
}
