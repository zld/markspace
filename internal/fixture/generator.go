// Package fixture generates deterministic MPEG-TS sample files covering
// table version changes, CC gaps and multiple programs. All sample data is
// produced in code; no external files are referenced.
package fixture

import (
	"fmt"
	"io"

	"tsmap/internal/psi"
	"tsmap/internal/ts"
)

// ProfileVersionsAndGap is the supported sample profile.
const ProfileVersionsAndGap = "versions-and-gap"

// PIDs and programs used by the generated sample.
const (
	TSID        = 1
	Prog1       = 1
	Prog1PMT    = 256
	Prog1Video  = 257
	Prog1Audio  = 258
	Prog2       = 2
	Prog2PMT    = 272
	Prog2Stream = 513
)

// Generate writes the named profile to w.
func Generate(w io.Writer, profile string) error {
	if profile != ProfileVersionsAndGap {
		return fmt.Errorf("unknown fixture profile %q", profile)
	}
	patV0 := buildPAT(TSID, 0)
	patV1 := buildPAT(TSID, 1)
	pmt1V0 := buildPMT(Prog1, 0, Prog1Video, []psi.Stream{
		{PID: Prog1Video, StreamType: 0x1b}, // H.264 video
		{PID: Prog1Audio, StreamType: 0x0f}, // AAC audio
	})
	pmt1V1 := buildPMT(Prog1, 1, Prog1Video, []psi.Stream{
		{PID: Prog1Video, StreamType: 0x1b},
		{PID: Prog1Audio, StreamType: 0x0f},
	})
	pmt2V0 := buildPMT(Prog2, 0, Prog2Stream, []psi.Stream{
		{PID: Prog2Stream, StreamType: 0x81}, // unknown/private type, kept as-is
	})
	videoPTS := int64(90000)
	audioPTS := int64(48000)
	type pkt struct {
		pid  uint16
		cc   byte
		data []byte // nil => section packet built from data via sectionPacket
	}
	pes := func(pid uint16, streamID byte, pts *int64, tag byte) []byte {
		body := make([]byte, 24)
		for i := range body {
			body[i] = tag + byte(i)
		}
		return pesPayload(streamID, pts, body)
	}
	stream := []pkt{
		{0, 0, patV0},
		{Prog1PMT, 0, pmt1V0},
		{Prog2PMT, 0, pmt2V0},
		{Prog1Video, 0, pes(Prog1Video, 0xE0, &videoPTS, 0x10)},
		{Prog1Audio, 0, pes(Prog1Audio, 0xC0, &audioPTS, 0x40)},
		{Prog2Stream, 0, pes(Prog2Stream, 0xBD, nil, 0x70)},
		{Prog1Video, 1, pes(Prog1Video, 0xE0, nil, 0x11)},
		{Prog1Audio, 1, pes(Prog1Audio, 0xC0, nil, 0x41)},
		{Prog1Video, 3, pes(Prog1Video, 0xE0, nil, 0x12)}, // CC 2 lost: gap of 1
		{Prog2Stream, 1, pes(Prog2Stream, 0xBD, nil, 0x71)},
		{Prog1Audio, 2, pes(Prog1Audio, 0xC0, nil, 0x42)},
		{Prog1Video, 4, pes(Prog1Video, 0xE0, nil, 0x13)},
		{0, 1, patV1},      // PAT version change
		{Prog1PMT, 1, pmt1V1}, // PMT version change (program 1)
		{Prog1Video, 5, pes(Prog1Video, 0xE0, nil, 0x14)},
		{Prog1Audio, 3, pes(Prog1Audio, 0xC0, nil, 0x43)},
		{Prog2Stream, 2, pes(Prog2Stream, 0xBD, nil, 0x72)},
		{0, 2, patV1},         // repeat: no version change
		{Prog1PMT, 2, pmt1V1}, // repeat: no version change
		{Prog2PMT, 1, pmt2V0}, // repeat: no version change
	}
	for _, p := range stream {
		var raw [ts.PacketSize]byte
		if p.pid == 0 || p.pid == Prog1PMT || p.pid == Prog2PMT {
			raw = sectionPacket(p.pid, p.cc, p.data)
		} else {
			raw = esPacket(p.pid, p.cc, p.data)
		}
		if _, err := w.Write(raw[:]); err != nil {
			return err
		}
	}
	return nil
}

// buildPAT builds a PAT section mapping the fixed two programs.
func buildPAT(tsid uint16, version byte) []byte {
	pmt1, pmt2 := uint16(Prog1PMT), uint16(Prog2PMT)
	body := []byte{
		0x00, Prog1, byte(0xE0|pmt1>>8), byte(pmt1),
		0x00, Prog2, byte(0xE0|pmt2>>8), byte(pmt2),
	}
	return section(0x00, tsid, version, body)
}

// buildPMT builds a PMT section for one program.
func buildPMT(program uint16, version byte, pcrPID uint16, streams []psi.Stream) []byte {
	body := []byte{0xE0 | byte(pcrPID>>8), byte(pcrPID), 0xF0, 0x00}
	for _, s := range streams {
		body = append(body, s.StreamType, 0xE0|byte(s.PID>>8), byte(s.PID), 0xF0, 0x00)
	}
	return section(0x02, program, version, body)
}

// section wraps a table body with the PSI header and CRC32.
func section(tableID byte, ext uint16, version byte, body []byte) []byte {
	sl := 5 + len(body) + 4 // header after length field + body + CRC
	out := []byte{
		tableID,
		0xB0 | byte(sl>>8), byte(sl),
		byte(ext >> 8), byte(ext),
		0xC0 | version<<1 | 1, // current_next=1
		0x00, 0x00, // section 0 of 0
	}
	out = append(out, body...)
	crc := psi.Checksum(out)
	return append(out, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

// sectionPacket places a whole section in one packet with pointer_field 0.
func sectionPacket(pid uint16, cc byte, sec []byte) [ts.PacketSize]byte {
	var pkt [ts.PacketSize]byte
	pkt[0] = ts.SyncByte
	pkt[1] = 0x40 | byte(pid>>8) // PUSI
	pkt[2] = byte(pid)
	pkt[3] = 0x10 | cc
	pkt[4] = 0x00 // pointer_field
	n := copy(pkt[5:], sec)
	for i := 5 + n; i < ts.PacketSize; i++ {
		pkt[i] = 0xFF
	}
	return pkt
}

// esPacket places a PES payload in one packet.
func esPacket(pid uint16, cc byte, payload []byte) [ts.PacketSize]byte {
	var pkt [ts.PacketSize]byte
	pkt[0] = ts.SyncByte
	pkt[1] = 0x40 | byte(pid>>8) // PUSI
	pkt[2] = byte(pid)
	pkt[3] = 0x10 | cc
	n := copy(pkt[4:], payload)
	for i := 4 + n; i < ts.PacketSize; i++ {
		pkt[i] = 0xFF
	}
	return pkt
}

// pesPayload builds a minimal PES packet with an optional PTS.
func pesPayload(streamID byte, pts *int64, payload []byte) []byte {
	hdr := []byte{0x00, 0x00, 0x01, streamID, 0x00, 0x00}
	opt := []byte{0x80, 0x00, 0x00}
	if pts != nil {
		opt[1] = 0x80 // PTS only
		opt[2] = 0x05
		opt = append(opt, encodePTS(0x2, *pts)...)
	}
	l := len(opt) + len(payload)
	hdr[4] = byte(l >> 8)
	hdr[5] = byte(l)
	out := append(hdr, opt...)
	return append(out, payload...)
}

// encodePTS encodes a 33-bit PTS with the given 4-bit prefix.
func encodePTS(prefix byte, pts int64) []byte {
	v := uint64(pts) & 0x1FFFFFFFF
	return []byte{
		prefix<<4 | byte((v>>30)&0x07)<<1 | 1,
		byte(v >> 22),
		byte((v>>15)&0x7F)<<1 | 1,
		byte(v >> 7),
		byte(v&0x7F)<<1 | 1,
	}
}
