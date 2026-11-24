package parse

import (
	"fmt"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
)

// TSPacketSize is the fixed MPEG-TS packet size.
const TSPacketSize = 188

// TSInfo is the parsed result of one MPEG-TS segment. PTS values are on
// the 90 kHz MPEG clock, which equals the master timescale, so they are
// stored directly as clock.Ticks.
type TSInfo struct {
	Path         string
	Packets      int
	PayloadBytes int64
	FirstPTS     clock.Ticks
	LastPTS      clock.Ticks
	HasPTS       bool
}

// ParseTS scans one MPEG-TS segment packet by packet (streaming-friendly:
// only one packet is inspected at a time) and extracts the PCR-free PES
// timestamp range plus payload statistics.
func ParseTS(path string, data []byte) (*TSInfo, error) {
	if len(data) == 0 {
		return nil, fault.New(fault.StageMediaPayload, "empty TS segment").At(path, 0)
	}
	if rem := len(data) % TSPacketSize; rem != 0 {
		return nil, fault.New(fault.StageMediaPayload,
			fmt.Sprintf("truncated TS packet (%d trailing bytes)", rem)).
			At(path, int64(len(data)-rem))
	}
	info := &TSInfo{Path: path}
	for off := 0; off < len(data); off += TSPacketSize {
		pkt := data[off : off+TSPacketSize]
		if pkt[0] != 0x47 {
			return nil, fault.New(fault.StageMediaPayload, "lost TS sync byte").
				At(path, int64(off))
		}
		info.Packets++
		pusi := pkt[1]&0x40 != 0
		afc := (pkt[3] >> 4) & 0x3
		payloadStart := 4
		if afc == 2 { // adaptation only, no payload
			continue
		}
		if afc == 3 { // adaptation field then payload
			payloadStart += 1 + int(pkt[4])
			if payloadStart > TSPacketSize {
				return nil, fault.New(fault.StageMediaPayload, "bad adaptation field length").
					At(path, int64(off))
			}
		}
		payload := pkt[payloadStart:]
		info.PayloadBytes += int64(len(payload))
		if !pusi || len(payload) < 14 {
			continue
		}
		if payload[0] != 0x00 || payload[1] != 0x00 || payload[2] != 0x01 {
			continue // not a PES start
		}
		ptsDTS := payload[7] >> 6
		if ptsDTS&0x2 == 0 {
			continue // no PTS
		}
		pts := decodePTS(payload[9:14])
		t := clock.Ticks(pts)
		if !info.HasPTS || t < info.FirstPTS {
			info.FirstPTS = t
		}
		if !info.HasPTS || t > info.LastPTS {
			info.LastPTS = t
		}
		info.HasPTS = true
	}
	if !info.HasPTS {
		return nil, fault.New(fault.StageMediaPayload, "TS segment carries no PTS").At(path, 0)
	}
	return info, nil
}

// decodePTS decodes the 33-bit MPEG PTS from its 5-byte encoding.
func decodePTS(b []byte) int64 {
	return int64(b[0]>>1&0x07)<<30 |
		int64(b[1])<<22 |
		int64(b[2]>>1&0x7f)<<15 |
		int64(b[3])<<7 |
		int64(b[4]>>1&0x7f)
}
