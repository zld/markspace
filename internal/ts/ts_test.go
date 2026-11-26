package ts

import (
	"bytes"
	"errors"
	"testing"

	"liveregress/internal/fault"
)

// builder emits a minimal single-program transport stream: PAT on PID 0,
// PMT on PID 0x1000, and one PES per access unit on the given PIDs.
type builder struct {
	buf     bytes.Buffer
	cc      map[uint16]uint8
	pmtPID  uint16
	streams []ESInfo
}

func newBuilder(streams ...ESInfo) *builder {
	return &builder{cc: map[uint16]uint8{}, pmtPID: 0x1000, streams: streams}
}

func (b *builder) packet(pid uint16, payload []byte, pusi bool, withAF bool) {
	cc := b.cc[pid]
	b.cc[pid]++
	for first := true; len(payload) > 0; first = false {
		var pkt [188]byte
		pkt[0] = 0x47
		pidBits := pid & 0x1FFF
		if pusi && first {
			pidBits |= 0x4000
		}
		pkt[1] = byte(pidBits >> 8)
		pkt[2] = byte(pidBits)
		chunk := payload
		if len(chunk) > 184 {
			chunk = chunk[:184]
		}
		payload = payload[len(chunk):]
		if !withAF && len(chunk) == 184 {
			pkt[3] = 0x10 | (cc & 0x0F)
			copy(pkt[4:], chunk)
			b.buf.Write(pkt[:])
			continue
		}
		// Adaptation field carries the stuffing so payload is last.
		afLen := 183 - len(chunk)
		pkt[3] = 0x30 | (cc & 0x0F)
		pkt[4] = byte(afLen)
		if afLen >= 1 {
			pkt[5] = 0x00
			for i := 6; i < 5+afLen; i++ {
				pkt[i] = 0xFF
			}
		}
		copy(pkt[5+afLen:], chunk)
		b.buf.Write(pkt[:])
	}
}

func (b *builder) psi(pid uint16, section []byte) {
	b.packet(pid, append([]byte{0x00}, section...), true, false)
}

func (b *builder) pat(program, pmtPID uint16) {
	body := []byte{byte(program >> 8), byte(program), 0xE0 | byte(pmtPID>>8), byte(pmtPID)}
	length := 5 + len(body) + 4
	sec := []byte{0x00, byte(0x80 | length>>8), byte(length), 0x00, 0x01, 0xC1, 0x00, 0x00}
	sec = append(sec, body...)
	sec = append(sec, 0, 0, 0, 0)
	b.psi(0x0000, sec)
}

func (b *builder) pmt(program uint16) {
	var body []byte
	for _, es := range b.streams {
		body = append(body, es.StreamType, 0xE0|byte(es.PID>>8), byte(es.PID), 0xF0, 0x00)
	}
	length := 9 + len(body) + 4
	sec := []byte{0x02, byte(0x80 | length>>8), byte(length), byte(program >> 8), byte(program), 0xC1, 0x00, 0x00, 0xE0, 0x00, 0xF0, 0x00}
	sec = append(sec, body...)
	sec = append(sec, 0, 0, 0, 0)
	b.psi(b.pmtPID, sec)
}

func (b *builder) pes(pid uint16, streamID byte, pts uint64, payload []byte) {
	var hdr []byte
	hdr = append(hdr, 0x00, 0x00, 0x01, streamID)
	hdr = append(hdr, 0x00, 0x00) // length patched below
	// byte 6: '10' marker; byte 7: PTS only; byte 8: header data length 5.
	hdr = append(hdr, 0x80, 0x80, 0x05)
	// 33-bit PTS in the 5-byte PES timestamp form.
	hdr = append(hdr,
		0x21|byte((pts>>29)&0x0E),
		byte(pts>>22),
		0x01|byte((pts>>14)&0xFE),
		byte(pts>>7),
		0x01|byte((pts<<1)&0xFE))
	// PES_packet_length counts every byte after the 6-byte prefix:
	// 3 optional PES header bytes + 5 PTS bytes + payload.
	l := 3 + 5 + len(payload)
	hdr[4], hdr[5] = byte(l>>8), byte(l)
	full := append(hdr, payload...)
	b.packet(pid, full, true, true)
}

func h264AU(key bool, payloadLen int, pts uint64) []byte {
	var au []byte
	au = append(au, 0, 0, 0, 1, 0x09, 0xF0) // AUD
	nalType := byte(naluSlice)
	if key {
		nalType = naluIDR
	}
	au = append(au, 0, 0, 1, 0x60|nalType) // nal_ref_idc=3, type in low 5 bits
	for i := 0; i < payloadLen; i++ {
		au = append(au, byte(i*3+1))
	}
	return au
}

func aacAU(n int) []byte {
	out := make([]byte, n)
	out[0] = 0x21
	for i := 1; i < n; i++ {
		out[i] = byte(0x40 + i)
	}
	return out
}

func (b *builder) bytes() []byte { return b.buf.Bytes() }

func TestDemuxSingleProgram(t *testing.T) {
	b := newBuilder(
		ESInfo{PID: 0x101, StreamType: StreamTypeH264, Language: "eng"},
		ESInfo{PID: 0x102, StreamType: StreamTypeAAC, Language: "eng"},
	)
	b.pat(1, b.pmtPID)
	b.pmt(1)
	for i := 0; i < 3; i++ {
		b.pes(0x101, 0xE0, uint64(90000*i), h264AU(i == 0, 50, uint64(90000*i)))
		b.pes(0x102, 0xC0, uint64(90000*i), aacAU(20))
	}
	d, err := DemuxFile(bytes.NewReader(b.bytes()), "seg.ts")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(d.Tracks))
	}
	video, audio := d.Tracks[0], d.Tracks[1]
	if video.Kind != "video" || audio.Kind != "audio" {
		t.Fatalf("kinds = %s,%s", video.Kind, audio.Kind)
	}
	if len(video.Samples) != 3 || len(audio.Samples) != 3 {
		t.Fatalf("samples = %d,%d", len(video.Samples), len(audio.Samples))
	}
	if video.TrackID != 1 || audio.TrackID != 2 {
		t.Fatalf("ids = %d,%d", video.TrackID, audio.TrackID)
	}
	if !video.Samples[0].Keyframe || video.Samples[1].Keyframe || video.Samples[2].Keyframe {
		t.Fatalf("keyframes = %v,%v,%v", video.Samples[0].Keyframe, video.Samples[1].Keyframe, video.Samples[2].Keyframe)
	}
	if video.Samples[1].PTS == nil || video.Samples[1].PTS.Ticks != 90000 || video.Samples[1].PTS.Timescale != 90000 {
		t.Fatalf("pts = %+v", video.Samples[1].PTS)
	}
	if video.Samples[0].AUOffset <= 0 {
		t.Fatalf("missing raw offset: %d", video.Samples[0].AUOffset)
	}
	// Verify the retained bytes really are at the reported offset.
	raw := b.bytes()
	off := int(video.Samples[0].AUOffset)
	if !bytes.Equal(raw[off:off+len(video.Samples[0].AUBytes)], video.Samples[0].AUBytes) {
		t.Fatal("AU bytes do not match raw input at recorded offset")
	}
}

func TestTruncatedTailPacket(t *testing.T) {
	b := newBuilder(ESInfo{PID: 0x101, StreamType: StreamTypeH264})
	b.pat(1, b.pmtPID)
	b.pmt(1)
	b.pes(0x101, 0xE0, 0, h264AU(true, 20, 0))
	full := b.bytes()
	cut := full[:len(full)-50]
	d, err := DemuxFile(bytes.NewReader(cut), "seg.ts")
	if err != nil {
		t.Fatalf("truncate should be recoverable, got %v", err)
	}
	if len(d.Faults) == 0 {
		t.Fatal("expected a recoverable truncation fault")
	}
	fe := d.Faults[len(d.Faults)-1]
	if !fe.Recoverable || !errors.Is(fe, fault.ErrTruncated) {
		t.Fatalf("fault = %v", fe)
	}
	if fe.Offset != int64(len(cut)-len(cut)%188) {
		t.Fatalf("offset = %d", fe.Offset)
	}
}

func TestCorruptSyncStops(t *testing.T) {
	b := newBuilder(ESInfo{PID: 0x101, StreamType: StreamTypeH264})
	b.pat(1, b.pmtPID)
	b.pmt(1)
	b.pes(0x101, 0xE0, 0, h264AU(true, 300, 0))
	raw := append([]byte(nil), b.bytes()...)
	raw[188*2] = 0x00
	_, err := DemuxFile(bytes.NewReader(raw), "seg.ts")
	if !errors.Is(err, fault.ErrCorrupt) {
		t.Fatalf("want corrupt, got %v", err)
	}
	fe, _ := fault.As(err)
	if fe.Offset != 188*2 {
		t.Fatalf("offset = %d", fe.Offset)
	}
}

func TestContinuityJumpIsFatal(t *testing.T) {
	b := newBuilder(ESInfo{PID: 0x101, StreamType: StreamTypeH264})
	b.pat(1, b.pmtPID)
	b.pmt(1)
	b.pes(0x101, 0xE0, 0, h264AU(true, 500, 0)) // spans several packets
	raw := append([]byte(nil), b.bytes()...)
	// Corrupt the continuity counter of the third media packet.
	for i := 2; i < len(raw)/188; i++ {
		if raw[i*188+1]&0x1F == 0x01 {
			raw[i*188+3] = (raw[i*188+3] & 0xF0) | 0x0B
			break
		}
	}
	_, err := DemuxFile(bytes.NewReader(raw), "seg.ts")
	if !errors.Is(err, fault.ErrCorrupt) {
		t.Fatalf("want continuity corrupt, got %v", err)
	}
}

func TestNoPSI(t *testing.T) {
	b := newBuilder()
	b.pes(0x101, 0xE0, 0, h264AU(true, 10, 0))
	_, err := DemuxFile(bytes.NewReader(b.bytes()), "seg.ts")
	if !errors.Is(err, fault.ErrMissing) {
		t.Fatalf("want missing, got %v", err)
	}
}

func TestParsePMTStreams(t *testing.T) {
	b := newBuilder(ESInfo{PID: 0x200, StreamType: StreamTypeH265}, ESInfo{PID: 0x201, StreamType: StreamTypeAC3})
	b.pmt(7)
	asm := NewSectionAssembler("x", 0x1000)
	raw := b.bytes()
	// The single PAT packet was not emitted; take the PMT packet directly.
	p, err := parsePacket(raw[:188], 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	secs, err := asm.Push(&p)
	if err != nil {
		t.Fatal(err)
	}
	if len(secs) != 1 {
		t.Fatalf("sections = %d", len(secs))
	}
	pmt, err := ParsePMT(secs[0], "x", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pmt.Streams) != 2 || pmt.Streams[0].PID != 0x200 || pmt.Streams[1].StreamType != StreamTypeAC3 {
		t.Fatalf("pmt = %+v", pmt)
	}
	if kind, ok := streamKind(StreamTypeAC3); ok {
		t.Fatalf("AC-3 should not be treated as copyable media, got %s", kind)
	}
}
