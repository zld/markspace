package ingest

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/pion/rtp"

	"tsindex/internal/fault"
)

// makePacket builds a minimal TS packet with the given PID, flags and CC.
func makePacket(pid uint16, pusi bool, cc uint8, payload []byte) [188]byte {
	var p [188]byte
	p[0] = SyncByte
	if pusi {
		p[1] = 0x40
	}
	p[1] |= byte(pid >> 8)
	p[2] = byte(pid)
	p[3] = 0x10 | (cc & 0x0F) // adaptation control 01: payload only
	copy(p[4:], payload)
	return p
}

func TestStreamReaderPacketBoundaryAndTruncation(t *testing.T) {
	good := makePacket(0x100, true, 0, make([]byte, 184))
	// Append a partial tail of 37 bytes (inside the required 1..187 range).
	stream := append(append([]byte{}, good[:]...), make([]byte, 37)...)
	rec := fault.NewRecorder()
	sr := NewStreamReader(bytes.NewReader(stream), rec)
	pk, err := sr.NextPacket()
	if err != nil || pk == nil || pk.PID != 0x100 || pk.Offset != 0 {
		t.Fatalf("first packet: %+v err=%v", pk, err)
	}
	if _, err := sr.NextPacket(); err != io.EOF {
		t.Fatalf("want EOF after truncation, got %v", err)
	}
	trunc := rec.All()
	if len(trunc) != 1 || trunc[0].Kind != fault.KindTruncated ||
		trunc[0].Offset != 188 || !trunc[0].Recoverable {
		t.Fatalf("truncation fault wrong: %+v", trunc)
	}
}

func TestStreamReaderCorruptSyncByte(t *testing.T) {
	bad := makePacket(0x100, false, 0, nil)
	bad[0] = 0x00
	rec := fault.NewRecorder()
	sr := NewStreamReader(bytes.NewReader(bad[:]), rec)
	if _, err := sr.NextPacket(); err != nil {
		t.Fatalf("corrupt packet must still demarcate: %v", err)
	}
	if rec.Count(fault.KindCorruptPacket) != 1 {
		t.Fatalf("want one corrupt-packet fault, got %+v", rec.All())
	}
}

func TestContinuityCounterGapRecordedNotRepaired(t *testing.T) {
	var buf bytes.Buffer
	for _, cc := range []uint8{0, 1, 4, 5} { // 1 -> 4 is a gap
		p := makePacket(0x200, false, cc, nil)
		buf.Write(p[:])
	}
	rec := fault.NewRecorder()
	sr := NewStreamReader(&buf, rec)
	count := 0
	for {
		_, err := sr.NextPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 4 {
		t.Fatalf("packets must not be inserted or dropped: got %d", count)
	}
	if rec.Count(fault.KindCCGap) != 1 {
		t.Fatalf("want exactly one CC gap, got %+v", rec.All())
	}
}

func TestRTPTransportRoundTrip(t *testing.T) {
	var encoded bytes.Buffer
	packets := [][188]byte{
		makePacket(0x100, true, 0, make([]byte, 184)),
		makePacket(0x100, false, 1, make([]byte, 184)),
	}
	var tsPayload bytes.Buffer
	for _, p := range packets {
		tsPayload.Write(p[:])
	}
	pkt := rtp.Packet{Header: rtp.Header{SSRC: 42, SequenceNumber: 7}, Payload: tsPayload.Bytes()}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(raw)))
	encoded.Write(l[:])
	encoded.Write(raw)

	rec := fault.NewRecorder()
	rt := NewRTPTransport(bytes.NewReader(encoded.Bytes()), rec)
	sr := NewStreamReader(rt, rec)
	n := 0
	for {
		p, err := sr.NextPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if p.PID != 0x100 || p.Raw[0] != SyncByte {
			t.Fatalf("RTP-decoded packet wrong: %+v", p)
		}
		n++
	}
	if n != 2 || len(rec.All()) != 0 {
		t.Fatalf("RTP roundtrip n=%d faults=%+v", n, rec.All())
	}
}

// psiTS wraps a section in PID-0/PID-pmt TS packets with a pointer field.
func psiTS(pid uint16, pusi bool, cc uint8, section []byte) [][188]byte {
	var out [][188]byte
	payload := section
	first := true
	for len(payload) > 0 || first {
		var p [188]byte
		p[0] = SyncByte
		if pusi && first {
			p[1] = 0x40
		}
		p[1] |= byte(pid >> 8)
		p[2] = byte(pid)
		p[3] = 0x10 | (cc & 0x0F)
		pos := 4
		if pusi && first {
			p[4] = 0 // pointer field
			pos = 5
		}
		n := copy(p[pos:], payload)
		payload = payload[n:]
		out = append(out, p)
		cc++
		first = false
	}
	return out
}

func patSection(version uint8, programs map[uint16]uint16) []byte {
	body := []byte{0x00, 0, 0, 0x00, 0x01, byte(0xC1 | (version << 1)), 0x00, 0x00}
	for prog, pid := range programs {
		body = append(body, byte(prog>>8), byte(prog), 0xE0|byte(pid>>8), byte(pid))
	}
	sectionLen := len(body) - 3 + 4
	body[1] = 0xB0 | byte(sectionLen>>8)
	body[2] = byte(sectionLen)
	crc := crc32MPEG2(body)
	return append(body, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func pmtSection(version, prog uint16, pmtPID uint16, streams map[uint16]uint8) []byte {
	body := []byte{0x02, 0, 0, byte(prog >> 8), byte(prog), byte(0xC1 | (version << 1)),
		0x00, 0x00, 0xE0, byte(pmtPID), 0xF0, 0x00}
	for esPID, streamType := range streams {
		body = append(body, streamType, 0xE0|byte(esPID>>8), byte(esPID), 0xF0, 0x00)
	}
	sectionLen := len(body) - 3 + 4
	body[1] = 0xB0 | byte(sectionLen>>8)
	body[2] = byte(sectionLen)
	crc := crc32MPEG2(body)
	return append(body, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func runPSI(t *testing.T, sections map[uint16][][]byte) (*fault.Recorder, *PSITracker) {
	t.Helper()
	var buf bytes.Buffer
	ccs := map[uint16]uint8{}
	// Stable PID order for deterministic tests.
	pids := []uint16{PIDPAT, 0x1000, 0x1001}
	for _, pid := range pids {
		for _, sec := range sections[pid] {
			for _, p := range psiTS(pid, true, ccs[pid], sec) {
				buf.Write(p[:])
				ccs[pid]++
			}
		}
	}
	rec := fault.NewRecorder()
	sr := NewStreamReader(&buf, rec)
	tr := NewPSITracker(rec)
	for {
		r, err := sr.NextPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		tr.Feed(r)
	}
	return rec, tr
}

func TestPSIPATVersionEpochAndCRC(t *testing.T) {
	progs := map[uint16]uint16{1: 0x1000, 2: 0x1001}
	secs := map[uint16][][]byte{
		PIDPAT: {patSection(0, progs), patSection(1, progs)},
		0x1000: {pmtSection(0, 1, 0x1000, map[uint16]uint8{0x0100: 0x1B})},
		0x1001: {pmtSection(0, 2, 0x1001, map[uint16]uint8{0x0200: 0x24})},
	}
	rec, tr := runPSI(t, secs)
	ep := tr.Current()
	if ep == nil || len(ep.Programs) != 2 {
		t.Fatalf("epoch programs wrong: %+v", ep)
	}
	p1 := ep.Programs[1]
	if s := p1.Streams[0x0100]; !s.Complete || s.Codec != "h264" {
		t.Fatalf("H.264 stream info wrong: %+v", s)
	}
	p2 := ep.Programs[2]
	if s := p2.Streams[0x0200]; !s.Complete || s.Codec != "hevc" {
		t.Fatalf("HEVC stream info wrong: %+v", s)
	}
	// Only the PAT v0->v1 change is a genuine update; first-time PMT/PAT
	// establishment must not inflate the version counter.
	if rec.Count(fault.KindPSIUpdate) != 1 {
		t.Fatalf("want exactly one version update, got %+v", rec.All())
	}
	if tr.VersionUpdates() != 1 {
		t.Fatalf("VersionUpdates()=%d want 1", tr.VersionUpdates())
	}
	if rec.Count(fault.KindBadCRC) != 0 {
		t.Fatalf("unexpected CRC faults: %+v", rec.All())
	}
}

func TestPSIBadCRCRejected(t *testing.T) {
	bad := patSection(0, map[uint16]uint16{1: 0x1000})
	bad[len(bad)-1] ^= 0xFF // corrupt the CRC
	rec, tr := runPSI(t, map[uint16][][]byte{PIDPAT: {bad}})
	if tr.Current() != nil {
		t.Fatal("corrupt PAT must not establish an epoch")
	}
	if rec.Count(fault.KindBadCRC) != 1 {
		t.Fatalf("want bad-crc fault, got %+v", rec.All())
	}
}

func TestPSIUnsupportedCodecMarkedIncomplete(t *testing.T) {
	secs := map[uint16][][]byte{
		PIDPAT: {patSection(0, map[uint16]uint16{1: 0x1000})},
		0x1000: {pmtSection(0, 1, 0x1000, map[uint16]uint8{0x0100: 0x06})},
	}
	_, tr := runPSI(t, secs)
	s := tr.Current().Programs[1].Streams[0x0100]
	if s.Complete || s.Codec != "other" {
		t.Fatalf("unsupported codec must be marked incomplete: %+v", s)
	}
}

func TestPSIPMTUpdateInvalidatesOnlyAffectedProgram(t *testing.T) {
	secs := map[uint16][][]byte{
		PIDPAT: {patSection(0, map[uint16]uint16{1: 0x1000, 2: 0x1001})},
		0x1000: {pmtSection(0, 1, 0x1000, map[uint16]uint8{0x0100: 0x1B}),
			pmtSection(1, 1, 0x1000, map[uint16]uint8{0x0100: 0x1B, 0x0101: 0x1B})},
		0x1001: {pmtSection(0, 2, 0x1001, map[uint16]uint8{0x0200: 0x24})},
	}
	_, tr := runPSI(t, secs)
	ep := tr.Current()
	if len(ep.ChangedPrograms) != 1 || ep.ChangedPrograms[0] != 1 {
		t.Fatalf("only program 1 must be invalidated: %+v", ep.ChangedPrograms)
	}
	if len(ep.Programs[2].Streams) != 1 {
		t.Fatal("unaffected program mapping changed")
	}
}

func TestRTPTransportRejectsMisalignedPayload(t *testing.T) {
	var encoded bytes.Buffer
	pkt := rtp.Packet{Payload: make([]byte, 190)} // not a multiple of 188
	raw, _ := pkt.Marshal()
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(raw)))
	encoded.Write(l[:])
	encoded.Write(raw)

	rec := fault.NewRecorder()
	rt := NewRTPTransport(bytes.NewReader(encoded.Bytes()), rec)
	io.ReadAll(rt)
	if rec.Count(fault.KindCorruptPacket) != 1 {
		t.Fatalf("want alignment fault, got %+v", rec.All())
	}
}
