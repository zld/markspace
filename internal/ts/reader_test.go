package ts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"mediad/internal/model"
)

// buildPacket assembles a minimal payload-only TS packet.
func buildPacket(pid uint16, cc uint8, pusi bool, payload []byte) [model.PacketSize]byte {
	var p [model.PacketSize]byte
	p[0] = model.SyncByte
	p[1] = byte(pid >> 8)
	if pusi {
		p[1] |= 0x40
	}
	p[2] = byte(pid)
	p[3] = 0x10 | (cc & 0x0F)
	copy(p[4:], payload)
	return p
}

func TestReaderHappyPathAndOffsets(t *testing.T) {
	p1 := buildPacket(0x100, 0, false, []byte("aaaa"))
	p2 := buildPacket(0x100, 1, true, []byte("bbbb"))
	r := NewReader(bytes.NewReader(append(p1[:], p2[:]...)))
	got, err := r.ReadPacket(context.Background())
	if err != nil || got.PID != 0x100 || got.Offset != 0 {
		t.Fatalf("first packet: %+v err=%v", got, err)
	}
	got, err = r.ReadPacket(context.Background())
	if err != nil || got.Offset != model.PacketSize || !got.PayloadStart {
		t.Fatalf("second packet offset=%d pusi=%v err=%v", got.Offset, got.PayloadStart, err)
	}
	if _, err = r.ReadPacket(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestReaderSyncByteErrorHasOffset(t *testing.T) {
	p1 := buildPacket(0x100, 0, false, nil)
	p2 := buildPacket(0x101, 1, false, nil)
	p2[0] = 0x00 // destroy sync byte of the second packet
	r := NewReader(bytes.NewReader(append(p1[:], p2[:]...)))
	if _, err := r.ReadPacket(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := r.ReadPacket(context.Background())
	var ce *model.CorruptError
	if !errors.As(err, &ce) || !errors.Is(err, model.ErrSyncByte) {
		t.Fatalf("expected CorruptError/ErrSyncByte, got %v", err)
	}
	if ce.Offset != model.PacketSize {
		t.Fatalf("offset = %d, want %d", ce.Offset, model.PacketSize)
	}
}

func TestReaderTruncatedTail(t *testing.T) {
	p1 := buildPacket(0x100, 0, false, nil)
	stream := append(p1[:], 0x47, 0x01, 0x02) // three stray bytes
	r := NewReader(bytes.NewReader(stream))
	if _, err := r.ReadPacket(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := r.ReadPacket(context.Background())
	var ce *model.CorruptError
	if !errors.As(err, &ce) || !errors.Is(err, model.ErrTruncated) {
		t.Fatalf("expected truncation error, got %v", err)
	}
	if ce.Offset != model.PacketSize {
		t.Fatalf("truncation offset = %d", ce.Offset)
	}
}

func TestContinuityGapsAndRepeats(t *testing.T) {
	tr := NewContinuityTracker()
	pid := uint16(0x200)
	tr.Observe(&model.Packet{PID: pid, Continuity: 5, HasPayload: true})
	if g := tr.Observe(&model.Packet{PID: pid, Continuity: 6, HasPayload: true}); g != nil {
		t.Fatal("expected no gap on normal increment")
	}
	g := tr.Observe(&model.Packet{PID: pid, Continuity: 9, HasPayload: true, Offset: 40})
	if g == nil || g.Repeated || g.Expected != 7 || g.Actual != 9 {
		t.Fatalf("expected gap 7->9, got %+v", g)
	}
	g = tr.Observe(&model.Packet{PID: pid, Continuity: 9, HasPayload: true, Offset: 60})
	if g == nil || !g.Repeated {
		t.Fatalf("expected repeated-packet event, got %+v", g)
	}
	// Adaptation-only packets never move the counter.
	if g := tr.Observe(&model.Packet{PID: pid, Continuity: 9, HasPayload: false}); g != nil {
		t.Fatal("adaptation-only packet must be ignored")
	}
}

// patPacket builds a PAT containing one program -> pmtPID mapping.
func patPacket(version, cc uint8, program, pmtPID uint16) [model.PacketSize]byte {
	body := []byte{
		0x00, // pointer field
		0x00, // table id PAT
		0xB0, 0x11, // section syntax indicator + length 17
		byte(program >> 8), byte(program), // transport stream id
		(version << 1) | 0x01, 0x00, 0x00, // version/current, section/last
		0x00, 0x01, // one program
		byte(pmtPID >> 8), byte(pmtPID), // PMT PID
		0x00, 0x00, 0x00, 0x00, // CRC placeholder
	}
	p := buildPacket(model.PIDPAT, cc, true, body)
	return p
}

// pmtPacket builds a PMT with one PCR PID and one elementary stream.
func pmtPacket(pid, program uint16, version, cc uint8, pcrPID, esPID uint16) [model.PacketSize]byte {
	body := []byte{
		0x00, // pointer
		0x02, // PMT table id
		0xB0, 0x12,
		byte(program >> 8), byte(program),
		(version << 1) | 0x01, 0x00, 0x00,
		byte(pcrPID >> 8), byte(pcrPID), // PCR PID
		0xF0, 0x00, // no program info
		0x1B, byte(esPID >> 8), byte(esPID), 0xF0, 0x00, // one video stream
		0x00, 0x00, 0x00, 0x00, // CRC
	}
	return buildPacket(pid, cc, true, body)
}

func TestPSIVersionSwitchAndMapping(t *testing.T) {
	tr := NewPSITracker()
	pat := patPacket(0, 0, 1, 0x1000)
	pmt := pmtPacket(0x1000, 1, 0, 0, 0x0100, 0x0200)
	if _, err := tr.Observe(parsePacket(pat, 0)); err != nil {
		t.Fatal(err)
	}
	if name, blocked := tr.Blocker(0x200); !blocked || name != "pmt:pid0x200" {
		t.Fatalf("expected PMT blocker before map, got %q %v", name, blocked)
	}
	if _, err := tr.Observe(parsePacket(pmt, 188)); err != nil {
		t.Fatal(err)
	}
	if !tr.IsMapped(0x200) || !tr.IsMapped(0x100) {
		t.Fatal("PCR and elementary PIDs must be mapped after PMT")
	}
	if m := tr.MapForPID(0x200); m == nil || m.ProgramNumber != 1 || m.Streams[0x200] != 0x1B {
		t.Fatalf("bad program map: %+v", m)
	}
	// Repeated identical version must not create an event.
	pmt2 := pmtPacket(0x1000, 1, 0, 1, 0x100, 0x200)
	ev, err := tr.Observe(parsePacket(pmt2, 376))
	if err != nil || ev != nil {
		t.Fatalf("same version must be a no-op, ev=%v err=%v", ev, err)
	}
	pmt3 := pmtPacket(0x1000, 1, 1, 2, 0x100, 0x201)
	ev, err = tr.Observe(parsePacket(pmt3, 564))
	if err != nil || ev == nil || ev.Version != 1 {
		t.Fatalf("expected PMT version-1 event, ev=%v err=%v", ev, err)
	}
	if tr.IsMapped(0x200) || !tr.IsMapped(0x201) {
		t.Fatal("version switch must atomically replace PID mapping")
	}
	if len(tr.Events) != 3 { // PAT v0, PMT v0, PMT v1
		t.Fatalf("events = %d, want 3: %+v", len(tr.Events), tr.Events)
	}
}
