// Package ts: PAT and PMT consumers.
package ts

import (
	"sort"
	"strconv"

	"example.com/assetpack/internal/fault"
)

func (p *psiParser) consumePAT(pkt *Packet, idx int, off int64, sec []byte) {
	if len(sec) < 12 || !sectionCurrentNext(sec) {
		return
	}
	version := sectionVersion(sec)
	body := sec[8 : len(sec)-4]
	if len(body)%4 != 0 {
		p.res.Faults.Add(fault.New(fault.StageParse, fault.KindPSIInvalid,
			"PAT body length %d is not a multiple of 4", len(body)).
			AtOffset(off).Recover(true))
		return
	}
	var programs []Program
	for i := 0; i+4 <= len(body); i += 4 {
		num := uint16(body[i])<<8 | uint16(body[i+1])
		pid := uint16(body[i+2]&0x1F)<<8 | uint16(body[i+3])
		if num == 0 {
			continue
		}
		programs = append(programs, Program{Number: num, PID: pid})
	}
	if p.seenPAT && version == p.res.PATVersion {
		if len(programs) != len(p.res.PAT) {
			p.res.Faults.Add(fault.New(fault.StageParse, fault.KindPSIInvalid,
				"PAT version %d repeated with %d programs, previously %d",
				version, len(programs), len(p.res.PAT)).
				AtOffset(off).Recover(true))
		}
		return
	}
	from := p.res.PATVersion
	note := ""
	if p.seenPAT {
		note = "version advanced"
	} else {
		from = version
	}
	p.res.PAT = programs
	p.res.PATVersion = version
	p.seenPAT = true
	p.res.Events = append(p.res.Events, VersionEvent{
		Table: "PAT", PID: pkt.PID, From: from, To: version,
		Offset: off, Packet: idx, Programs: len(programs), CRCValid: true, Note: note,
	})
}

func (p *psiParser) consumePMT(pkt *Packet, idx int, off int64, sec []byte) {
	if len(sec) < 16 || !sectionCurrentNext(sec) {
		return
	}
	version := sectionVersion(sec)
	program := uint16(sec[3])<<8 | uint16(sec[4])
	pcrPID := uint16(sec[8]&0x1F)<<8 | uint16(sec[9])
	infoLen := int(sec[10]&0x0F)<<8 | int(sec[11])
	pos := 12 + infoLen
	if pos > len(sec)-4 {
		p.res.Faults.Add(fault.New(fault.StageParse, fault.KindPSIInvalid,
			"PMT program_info_length overruns section on PID %d", pkt.PID).
			AtOffset(off).Recover(true))
		return
	}
	var streams []PMTStream
	for pos+5 <= len(sec)-4 {
		st := sec[pos]
		pid := uint16(sec[pos+1]&0x1F)<<8 | uint16(sec[pos+2])
		esLen := int(sec[pos+3]&0x0F)<<8 | int(sec[pos+4])
		pos += 5
		if pos+esLen > len(sec)-4 {
			p.res.Faults.Add(fault.New(fault.StageParse, fault.KindPSIInvalid,
				"PMT ES_info_length overruns section on PID %d", pkt.PID).
				AtOffset(off).Recover(true))
			return
		}
		streams = append(streams, PMTStream{
			StreamType:  st,
			PID:         pid,
			Language:    languageFromDescriptors(sec[pos : pos+esLen]),
			Descriptors: parseDescriptors(sec[pos : pos+esLen]),
		})
		pos += esLen
	}
	existing := p.res.PMTs[pkt.PID]
	if p.seenPMT[pkt.PID] && existing != nil && existing.Version == version {
		return
	}
	from := version
	note := ""
	if existing != nil {
		from = existing.Version
		note = "version advanced"
	}
	pmt := &PMT{
		ProgramNumber: program, PCRPID: pcrPID, Version: version,
		Streams: streams, SectionOffset: off,
	}
	p.res.PMTs[pkt.PID] = pmt
	p.res.History = append(p.res.History, VersionedPMT{PMT: pmt, Offset: off, Packet: idx})
	p.res.Current = pmt
	p.seenPMT[pkt.PID] = true
	p.res.Events = append(p.res.Events, VersionEvent{
		Table: "PMT", PID: pkt.PID, From: from, To: version,
		Offset: off, Packet: idx, PCRPID: pcrPID,
		Streams: len(streams), CRCValid: true, Note: note,
	})
}

func parseDescriptors(raw []byte) []Descriptor {
	var out []Descriptor
	for len(raw) >= 2 {
		tag := raw[0]
		l := int(raw[1])
		if 2+l > len(raw) {
			break
		}
		out = append(out, Descriptor{Tag: tag, Data: append([]byte(nil), raw[2:2+l]...)})
		raw = raw[2+l:]
	}
	return out
}

func languageFromDescriptors(raw []byte) string {
	for _, d := range parseDescriptors(raw) {
		if d.Tag == 0x0A && len(d.Data) == 3 {
			return string(d.Data)
		}
	}
	return ""
}

// SortedPMTPIDs returns the discovered PMT PIDs in ascending order.

func sectionTableID(sec []byte) uint8 { return sec[0] }

func sectionVersion(sec []byte) uint8 { return (sec[5] >> 1) & 0x1F }

func sectionCurrentNext(sec []byte) bool { return sec[5]&0x01 != 0 }

func (r *PSIResult) SortedPMTPIDs() []uint16 {
	pids := make([]uint16, 0, len(r.PMTs))
	for pid := range r.PMTs {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	return pids
}

// Description returns a stable one-line summary of the map.
func (m *PMT) Description() string {
	if m == nil {
		return "no active PMT"
	}
	s := "PMT v" + strconv.Itoa(int(m.Version)) + " program " + strconv.Itoa(int(m.ProgramNumber))
	if a, ok := m.AudioStream(); ok {
		s += " audio pid " + strconv.Itoa(int(a.PID))
	}
	return s
}

// ParsePSI reassembles PSI sections from trusted packets only.  Packets whose
// continuity could not be proven never contribute section bytes, so damaged
// data cannot masquerade as a valid mapping.
func ParsePSI(s *Stream) *PSIResult {
	res := &PSIResult{Faults: &fault.List{}, PMTs: map[uint16]*PMT{}}
	if s == nil {
		return res
	}
	p := &psiParser{stream: s, res: res, partial: map[uint16]*sectionBuf{}, seenPMT: map[uint16]bool{}}
	for i := range s.Packets {
		pkt := &s.Packets[i]
		if pkt.Payload == nil || !pkt.Trusted {
			continue
		}
		if pkt.PUSI {
			if tbl, ok := firstSectionTableID(pkt.Payload); ok && (tbl == TableIDPAT || tbl == TableIDPMT) {
				p.feed(pkt, i)
			}
			continue
		}
		if _, inflight := p.partial[pkt.PID]; inflight {
			p.feed(pkt, i)
		}
	}
	res.Faults.Merge(s.Faults)
	return res
}

func (p *psiParser) dispatchParsed(pkt *Packet, idx int, off int64, sec []byte) {
	switch sectionTableID(sec) {
	case TableIDPAT:
		p.consumePAT(pkt, idx, off, sec)
	case TableIDPMT:
		p.consumePMT(pkt, idx, off, sec)
	default:
	}
}
