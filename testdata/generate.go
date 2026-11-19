//go:build ignore

// Command generate writes deterministic MPEG-TS fixtures exercising multiple
// programs, PAT/PMT version changes, and continuity anomalies. Every byte is
// produced locally; no external sample is read.
//
// The default -out stream is valid at the container level: it contains packet
// loss, a duplicate, an adaptation-field-only packet and a transport-error
// packet, but every PSI section is intact, so `tsinspect -json` exits 0.
// Optional variants add the damaged-input cases used by scripts/acceptance.sh:
//
//	-corrupt-out    adds a PMT with a broken CRC and an orphan PMT
//	-midstream-out  starts the same stream after its first PAT/PMT epoch
//	-truncate-out   appends a partial packet tail
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

const (
	packetSize = 188
	tsid       = 1
)

// crc32 is the MPEG-2 PSI CRC (poly 0x04C11DB7, init 0xFFFFFFFF).
func crc32(data []byte) uint32 {
	c := uint32(0xffffffff)
	for _, b := range data {
		c ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if c&0x80000000 != 0 {
				c = (c << 1) ^ 0x04c11db7
			} else {
				c <<= 1
			}
		}
	}
	return c
}

type streamDef struct {
	pid        uint16
	streamType uint8
	packets    int
}

type programDef struct {
	number  uint16
	pmtPID  uint16
	pcrPID  uint16
	streams []streamDef
}

func (p programDef) withStreams(s ...streamDef) programDef {
	p.streams = s
	return p
}

func section(tableID uint8, ext uint16, version uint8, secNum, lastSec uint8, body []byte) []byte {
	length := 5 + len(body) + 4
	s := []byte{
		tableID,
		0xb0 | byte(length>>8), byte(length),
		byte(ext >> 8), byte(ext),
		0xc0 | version<<1 | 1, // current_next_indicator = 1
		secNum, lastSec,
	}
	s = append(s, body...)
	c := crc32(s)
	return append(s, byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
}

func patSection(version uint8, programs []programDef) []byte {
	var body []byte
	for _, p := range programs {
		body = append(body, byte(p.number>>8), byte(p.number), 0xe0|byte(p.pmtPID>>8), byte(p.pmtPID))
	}
	return section(0x00, tsid, version, 0, 0, body)
}

func pmtSection(p programDef, version uint8) []byte {
	body := []byte{0xe0 | byte(p.pcrPID>>8), byte(p.pcrPID), 0xf0, 0x00}
	for _, es := range p.streams {
		body = append(body, es.streamType, 0xe0|byte(es.pid>>8), byte(es.pid), 0xf0, 0x00)
	}
	return section(0x02, p.number, version, 0, 0, body)
}

type packetizer struct {
	pid uint16
	cc  uint8
}

func (pz *packetizer) packets(sections []byte) [][packetSize]byte {
	var out [][packetSize]byte
	for first := true; len(sections) > 0; {
		var pkt [packetSize]byte
		pkt[0] = 0x47
		pkt[1] = byte(pz.pid >> 8)
		pkt[2] = byte(pz.pid)
		pkt[3] = 0x10 | pz.cc&0x0f
		payload := pkt[4:]
		if first {
			pkt[1] |= 0x40
			payload, first = payload[1:], false
		}
		n := copy(payload, sections)
		for i := n; i < len(payload); i++ {
			payload[i] = 0xff
		}
		sections = sections[n:]
		pz.cc = (pz.cc + 1) & 0x0f
		out = append(out, pkt)
	}
	return out
}

// payloadPackets emits PES-like payload packets for one elementary stream.
// dropAt/dropCount simulate a burst of lost packets (the counters still
// advance); duplicateAt repeats one packet byte-for-byte; afOnlyAt inserts an
// adaptation-field-only packet that legitimately keeps the same counter.
func payloadPackets(pid uint16, cc *uint8, count int, seed byte, dropAt, dropCount, duplicateAt, afOnlyAt int) [][packetSize]byte {
	var out [][packetSize]byte
	for i, emitted := 0, 0; i < count; i++ {
		if dropAt >= 0 && i >= dropAt && i < dropAt+dropCount {
			*cc = (*cc + 1) & 0x0f
			continue
		}
		var pkt [packetSize]byte
		pkt[0] = 0x47
		pkt[1] = byte(pid >> 8)
		pkt[2] = byte(pid)
		if emitted == 0 {
			pkt[1] |= 0x40
		}
		pkt[3] = 0x10 | *cc
		for j := 4; j < packetSize; j++ {
			pkt[j] = seed + byte(j)
		}
		out = append(out, pkt)
		if i == duplicateAt {
			out = append(out, pkt) // exact duplicate: same counter, same bytes
		}
		if i == afOnlyAt { // adaptation field only, counter unchanged
			var af [packetSize]byte
			af[0], af[1], af[2] = 0x47, byte(pid>>8), byte(pid)
			af[3], af[4] = 0x20|*cc, 183
			out = append(out, af)
		}
		emitted++
		*cc = (*cc + 1) & 0x0f
	}
	return out
}

// teiPacket marks one packet with the transport_error_indicator.
func teiPacket(pid uint16, cc *uint8) [packetSize]byte {
	var pkt [packetSize]byte
	pkt[0], pkt[1], pkt[2] = 0x47, 0x80|byte(pid>>8), byte(pid)
	pkt[3] = 0x10 | *cc
	for j := 4; j < packetSize; j++ {
		pkt[j] = 0xee
	}
	*cc = (*cc + 1) & 0x0f
	return pkt
}

func main() {
	out := flag.String("out", "", "write the main sample stream")
	corruptOut := flag.String("corrupt-out", "", "write a stream with broken CRC and orphan PMT sections")
	midstreamOut := flag.String("midstream-out", "", "write a stream that starts mid-program")
	truncateOut := flag.String("truncate-out", "", "write a stream ending in a partial packet")
	flag.Parse()
	if *out == "" && *corruptOut == "" && *midstreamOut == "" && *truncateOut == "" {
		log.Fatal("at least one of -out, -corrupt-out, -midstream-out, -truncate-out is required")
	}

	p101 := programDef{number: 101, pmtPID: 0x1000, pcrPID: 0x0100, streams: []streamDef{
		{pid: 0x0100, streamType: 0x1b, packets: 90}, // H.264
		{pid: 0x0101, streamType: 0x0f, packets: 70}, // AAC
	}}
	p102 := programDef{number: 102, pmtPID: 0x1001, pcrPID: 0x0200, streams: []streamDef{
		{pid: 0x0200, streamType: 0x1b, packets: 60},
		{pid: 0x0201, streamType: 0x03, packets: 30},
	}}
	p103 := programDef{number: 103, pmtPID: 0x1002, pcrPID: 0x0300, streams: []streamDef{
		{pid: 0x0300, streamType: 0x24, packets: 40}, // HEVC
	}}
	p101v1 := p101.withStreams(
		streamDef{pid: 0x0100, streamType: 0x1b, packets: 40},
		streamDef{pid: 0x0101, streamType: 0x0f, packets: 30},
		streamDef{pid: 0x0102, streamType: 0x0f, packets: 30},
	)

	pat := &packetizer{pid: 0x0000}
	pmt101 := &packetizer{pid: p101.pmtPID}
	pmt102 := &packetizer{pid: p102.pmtPID}
	pmt103 := &packetizer{pid: p103.pmtPID}

	var stream [][packetSize]byte
	add := func(ps [][packetSize]byte) { stream = append(stream, ps...) }

	cc100, cc101 := uint8(0), uint8(5)
	cc200, cc201 := uint8(2), uint8(9)
	cc300, cc102, cc500 := uint8(1), uint8(0), uint8(0)

	// Epoch 1: PAT and PMTs version 0 for three programs.
	add(pat.packets(patSection(0, []programDef{p101, p102, p103})))
	add(pmt101.packets(pmtSection(p101, 0)))
	add(pmt102.packets(pmtSection(p102, 0)))
	add(pmt103.packets(pmtSection(p103, 0)))
	firstEpochEnd := len(stream)

	// Program 101 video: a two-packet loss, a duplicate, and an AF-only packet.
	add(payloadPackets(0x0100, &cc100, 55, 0x10, 20, 2, 35, 45))
	add(payloadPackets(0x0101, &cc101, 40, 0x40, -1, 0, -1, -1))
	add(payloadPackets(0x0200, &cc200, 35, 0x70, -1, 0, -1, -1))
	add(payloadPackets(0x0201, &cc201, 20, 0x90, -1, 0, -1, -1))
	add(payloadPackets(0x0300, &cc300, 25, 0xb0, -1, 0, -1, -1))
	// A PID that belongs to no program; it must never enter a slice.
	add(payloadPackets(0x0500, &cc500, 25, 0xd0, -1, 0, -1, -1))
	// One transport-error packet on program 102's video PID.
	tei := teiPacket(0x0200, &cc200)
	add([][packetSize]byte{tei})

	// Same-version retransmissions must not be counted as version changes.
	add(pat.packets(patSection(0, []programDef{p101, p102, p103})))
	add(pmt101.packets(pmtSection(p101, 0)))

	// Epoch 2: PAT version 1 removes program 103; PMT101 version 1 adds 0x0102.
	add(payloadPackets(0x0100, &cc100, 20, 0x10, -1, 0, -1, -1))
	add(payloadPackets(0x0101, &cc101, 15, 0x40, -1, 0, -1, -1))
	add(pat.packets(patSection(1, []programDef{p101v1, p102})))
	add(pmt101.packets(pmtSection(p101v1, 1)))
	add(pmt102.packets(pmtSection(p102, 0)))
	add(payloadPackets(0x0102, &cc102, 30, 0xc0, -1, 0, -1, -1))
	add(payloadPackets(0x0100, &cc100, 20, 0x10, -1, 0, -1, -1))
	add(payloadPackets(0x0101, &cc101, 15, 0x40, -1, 0, -1, -1))
	add(payloadPackets(0x0200, &cc200, 20, 0x70, -1, 0, -1, -1))
	add(payloadPackets(0x0500, &cc500, 20, 0xd0, -1, 0, -1, -1))

	// Final retransmission so the trailing table state is unambiguous.
	add(pat.packets(patSection(1, []programDef{p101v1, p102})))
	add(pmt101.packets(pmtSection(p101v1, 1)))

	write := func(path string, data [][packetSize]byte, extra []byte) {
		if path == "" {
			return
		}
		f, err := os.Create(path)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		for _, pkt := range data {
			if _, err := f.Write(pkt[:]); err != nil {
				log.Fatal(err)
			}
		}
		if len(extra) > 0 {
			if _, err := f.Write(extra); err != nil {
				log.Fatal(err)
			}
		}
		fmt.Printf("wrote %s: %d packets\n", path, len(data))
	}

	write(*out, stream, nil)
	if *midstreamOut != "" {
		// Starting after the first epoch leaves elementary packets ahead of
		// any table, exercising the slicer's pre-table cache.
		write(*midstreamOut, stream[firstEpochEnd:], nil)
	}
	if *truncateOut != "" {
		write(*truncateOut, stream, []byte{0x47, 0x00, 0x10, 0x00, 0x00, 0x00, 0x00})
	}
	if *corruptOut != "" {
		corrupt := make([][packetSize]byte, 0, len(stream)+16)
		corrupt = append(corrupt, stream...)
		bad := pmtSection(p101v1, 1)
		bad[len(bad)-1] ^= 0xff // broken CRC
		badPat := patSection(1, []programDef{p101v1, p102})
		badPat[len(badPat)-1] ^= 0xff
		pc := &packetizer{pid: p101.pmtPID, cc: pmt101.cc}
		corrupt = append(corrupt, pc.packets(bad)...)
		pc = &packetizer{pid: 0x0000, cc: pat.cc}
		corrupt = append(corrupt, pc.packets(badPat)...)
		// Orphan PMT: a PID no PAT refers to.
		orphan := programDef{number: 199, pmtPID: 0x1f00, pcrPID: 0x1f01, streams: []streamDef{
			{pid: 0x1f01, streamType: 0x1b, packets: 1},
		}}
		orphanPZ := &packetizer{pid: 0x1f00}
		corrupt = append(corrupt, orphanPZ.packets(pmtSection(orphan, 0))...)
		write(*corruptOut, corrupt, nil)
	}
}
