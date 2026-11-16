package ts

import (
	"fmt"
	"io"
)

// PESHeader is the parsed prefix of a PES packet.
type PESHeader struct {
	StreamID      byte
	PTS           int64 // raw 33-bit, -1 when absent
	PayloadOffset int   // elementary-stream offset within the PUSI payload
}

// ParsePES parses a PES header from the payload of a PUSI packet.
func ParsePES(payload []byte) (*PESHeader, error) {
	if len(payload) < 9 || payload[0] != 0 || payload[1] != 0 || payload[2] != 1 {
		return nil, fmt.Errorf("not a PES start")
	}
	h := &PESHeader{StreamID: payload[3], PTS: -1}
	if payload[7]&0x80 != 0 { // PTS present
		if len(payload) < 14 {
			return nil, fmt.Errorf("short PES header")
		}
		h.PTS = decodePTS(payload[9:14])
	}
	h.PayloadOffset = 9 + int(payload[8])
	if h.PayloadOffset > len(payload) {
		h.PayloadOffset = len(payload)
	}
	return h, nil
}

func decodePTS(b []byte) int64 {
	return int64(b[0]>>1&0x07)<<30 |
		int64(b[1])<<22 |
		int64(b[2]>>1)<<15 |
		int64(b[3])<<7 |
		int64(b[4]>>1)
}

// EncodePTS appends the 5-byte 33-bit PTS encoding to dst.
func EncodePTS(dst []byte, pts int64) []byte {
	return append(dst,
		0x20|(byte((pts>>30)&0x07)<<1)|1,
		byte(pts>>22),
		(byte((pts>>15)&0xFF)<<1)|1,
		byte(pts>>7),
		(byte(pts&0x7F)<<1)|1,
	)
}

// PTSUnwrapper converts raw 33-bit PTS values to a monotonic 64-bit
// timeline, detecting wraparound.
type PTSUnwrapper struct {
	init    bool
	last    int64
	wraps   int64
	Wrapped bool // set once a wraparound has been observed
}

// Unwrap extends pts across 33-bit wraparound boundaries.
func (u *PTSUnwrapper) Unwrap(pts int64) int64 {
	if !u.init {
		u.init, u.last = true, pts
		return pts
	}
	if pts < u.last-(1<<32) {
		u.wraps++
		u.Wrapped = true
	}
	u.last = pts
	return pts + u.wraps*(1<<33)
}

// HasIDR reports whether payload (the start of a PES elementary stream)
// begins an H.264 IDR access unit (Annex-B NAL of type 5).
func HasIDR(payload []byte) bool {
	for i := 0; i+4 < len(payload); i++ {
		if payload[i] != 0 || payload[i+1] != 0 {
			continue
		}
		nal := -1
		if payload[i+2] == 1 {
			nal = i + 3
		} else if payload[i+2] == 0 && payload[i+3] == 1 {
			nal = i + 4
		}
		if nal >= 0 && nal < len(payload) && payload[nal]&0x1F == 5 {
			return true
		}
	}
	return false
}

// ProgramMap tracks PAT/PMT sections to identify the video PID.
type ProgramMap struct {
	pmtPID int
	Video  int // -1 until discovered
}

// NewProgramMap returns an empty program map.
func NewProgramMap() *ProgramMap { return &ProgramMap{pmtPID: -1, Video: -1} }

// PMTPID returns the PMT PID, or -1 before the PAT has been seen.
func (m *ProgramMap) PMTPID() int { return m.pmtPID }

// Feed inspects a packet and updates the map.
func (m *ProgramMap) Feed(p *Packet) {
	if !p.PUSI || len(p.Payload) == 0 {
		return
	}
	switch {
	case p.PID == 0:
		m.parsePAT(p.Payload)
	case m.pmtPID >= 0 && p.PID == uint16(m.pmtPID):
		m.parsePMT(p.Payload)
	}
}

func skipPointer(b []byte) []byte {
	if len(b) == 0 || 1+int(b[0]) >= len(b) {
		return nil
	}
	return b[1+int(b[0]):]
}

func (m *ProgramMap) parsePAT(b []byte) {
	s := skipPointer(b)
	if len(s) < 12 || s[0] != 0x00 {
		return
	}
	m.pmtPID = int(s[10]&0x1F)<<8 | int(s[11])
}

func (m *ProgramMap) parsePMT(b []byte) {
	s := skipPointer(b)
	if len(s) < 16 || s[0] != 0x02 {
		return
	}
	pos := 12 + (int(s[10]&0x0F)<<8 | int(s[11]))
	for pos+5 <= len(s) {
		st := s[pos]
		pid := int(s[pos+1]&0x1F)<<8 | int(s[pos+2])
		if st == 0x1B || st == 0x24 { // H.264 / HEVC
			m.Video = pid
			return
		}
		pos += 5 + (int(s[pos+3]&0x0F)<<8 | int(s[pos+4]))
	}
}

// ScanPTS streams a TS input and returns the first/last unwrapped PTS,
// the number of PTS-bearing PES packets and any stream anomalies. It is
// used by the checker to re-verify published segments.
func ScanPTS(r io.Reader) (first, last int64, count int, anomalies []Anomaly, err error) {
	rd := NewReader(r)
	pm := NewProgramMap()
	var uw PTSUnwrapper
	probed := 0
	for {
		p, e := rd.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return 0, 0, 0, rd.Anomalies(), e
		}
		pm.Feed(p)
		if pm.Video < 0 {
			// Fallback: adopt the first PID carrying PES with PTS.
			if probed++; probed > 500 && p.PUSI {
				if h, pe := ParsePES(p.Payload); pe == nil && h.PTS >= 0 {
					pm.Video = int(p.PID)
				}
			}
			continue
		}
		if !p.PUSI || p.PID != uint16(pm.Video) {
			continue
		}
		h, pe := ParsePES(p.Payload)
		if pe != nil || h.PTS < 0 {
			continue
		}
		v := uw.Unwrap(h.PTS)
		if count == 0 {
			first = v
		}
		last = v
		count++
	}
	return first, last, count, rd.Anomalies(), nil
}
