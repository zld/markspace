package ts

import (
	"fmt"

	merr "mediapack/internal/errors"
)

// PAT is a parsed Program Association Table.
type PAT struct {
	Version  byte
	TSID     uint16
	Programs map[uint16]uint16 // program number -> PMT PID
	CRCValid bool
}

// PMTStream is one elementary stream entry.
type PMTStream struct {
	StreamType byte
	PID        uint16
}

// PMT is a parsed Program Map Table.
type PMT struct {
	Version       byte
	ProgramNumber uint16
	PCRPID        uint16
	Streams       []PMTStream
	CRCValid      bool
}

// ParsePAT decodes one complete PAT section.
func ParsePAT(sec []byte) *PAT {
	p := &PAT{Programs: map[uint16]uint16{}, CRCValid: CheckSectionCRC(sec)}
	if len(sec) < 8 {
		return p
	}
	p.TSID = uint16(sec[3])<<8 | uint16(sec[4])
	p.Version = (sec[5] >> 1) & 0x1F
	for i := 8; i+4 <= len(sec)-4; i += 4 {
		num := uint16(sec[i])<<8 | uint16(sec[i+1])
		pid := uint16(sec[i+2]&0x1F)<<8 | uint16(sec[i+3])
		if num != 0 {
			p.Programs[num] = pid
		}
	}
	return p
}

// ParsePMT decodes one complete PMT section.
func ParsePMT(sec []byte) *PMT {
	m := &PMT{CRCValid: CheckSectionCRC(sec)}
	if len(sec) < 12 {
		return m
	}
	m.ProgramNumber = uint16(sec[3])<<8 | uint16(sec[4])
	m.Version = (sec[5] >> 1) & 0x1F
	m.PCRPID = uint16(sec[8]&0x1F)<<8 | uint16(sec[9])
	pil := int(uint16(sec[10]&0x0F)<<8 | uint16(sec[11]))
	pos := 12 + pil
	end := len(sec) - 4 // exclude CRC
	for pos+5 <= end {
		st := sec[pos]
		pid := uint16(sec[pos+1]&0x1F)<<8 | uint16(sec[pos+2])
		esl := int(uint16(sec[pos+3]&0x0F)<<8 | uint16(sec[pos+4]))
		m.Streams = append(m.Streams, PMTStream{StreamType: st, PID: pid})
		pos += 5 + esl
	}
	return m
}

type sectionBuilder struct {
	buf  []byte
	need int
}

// PSIParser reassembles PSI sections per PID (single-packet sections in the
// generated fixtures, plus continuation support) with bounded buffering.
type PSIParser struct {
	builders map[uint16]*sectionBuilder
}

// NewPSIParser creates a section reassembler.
func NewPSIParser() *PSIParser {
	return &PSIParser{builders: map[uint16]*sectionBuilder{}}
}

const maxSection = 4096

// Sections feeds one packet and returns any complete sections.
func (p *PSIParser) Sections(pkt *Packet) [][]byte {
	if pkt == nil || len(pkt.Payload) == 0 {
		return nil
	}
	var out [][]byte
	if pkt.PUSI {
		pointer := int(pkt.Payload[0])
		start := 1 + pointer
		if start > len(pkt.Payload) {
			start = len(pkt.Payload)
		}
		sb := &sectionBuilder{}
		p.builders[pkt.PID] = sb
		p.feed(sb, pkt.Payload[start:], &out)
		return out
	}
	if sb := p.builders[pkt.PID]; sb != nil {
		p.feed(sb, pkt.Payload, &out)
	}
	return out
}

func (p *PSIParser) feed(sb *sectionBuilder, chunk []byte, out *[][]byte) {
	if len(chunk) == 0 {
		return
	}
	sb.buf = append(sb.buf, chunk...)
	if sb.need == 0 && len(sb.buf) >= 3 {
		slen := int(uint16(sb.buf[1]&0x0F)<<8 | uint16(sb.buf[2]))
		sb.need = 3 + slen
		if sb.need < 3 || sb.need > maxSection {
			sb.buf, sb.need = nil, 0
			return
		}
	}
	if sb.need > 0 && len(sb.buf) >= sb.need {
		sec := make([]byte, sb.need)
		copy(sec, sb.buf[:sb.need])
		*out = append(*out, sec)
		sb.buf, sb.need = nil, 0
	}
}

// Mapping is one complete PAT/PMT signalling state.
type Mapping struct {
	PATVersion  int               `json:"pat_version"`
	PMTVersion  int               `json:"pmt_version"`
	PMTPID      uint16            `json:"pmt_pid"`
	Programs    map[uint16]uint16 `json:"programs"`
	PCRPID      uint16            `json:"pcr_pid"`
	VideoPID    uint16            `json:"video_pid"`
	AudioPID    uint16            `json:"audio_pid"`
	StreamPIDs  []uint16          `json:"stream_pids"`
	Complete    bool              `json:"complete"`
	SinceTick   uint64            `json:"since_tick"`
	SinceOffset int64             `json:"since_offset"`
}

// MappingGap is an interval where a version switch / CRC failure left the
// active mapping incomplete.
type MappingGap struct {
	Reason      string `json:"reason"`
	StartOffset int64  `json:"start_offset"`
	EndOffset   int64  `json:"end_offset"`
	StartTick   uint64 `json:"start_tick"`
	EndTick     uint64 `json:"end_tick"`
	Resolved    bool   `json:"resolved"`
	FromVersion int    `json:"from_version"`
	ToVersion   int    `json:"to_version"`
}

// Tracker tracks PAT/PMT versions, mappings and incompleteness gaps.
type Tracker struct {
	Parser  *PSIParser
	Issues  *merr.Book
	NowTick uint64

	current    *Mapping
	hasMapping bool
	patVersion int
	hasPAT     bool
	gap        *MappingGap
	pmtSeen    map[uint16]bool
	pmtVersion map[uint16]int
	pmtPIDs    map[uint16]bool

	// History contains every completed mapping in order.
	History []Mapping
	// Gaps contains every (closed or still open) incompleteness episode.
	Gaps []MappingGap
}

// NewTracker creates a PSI version tracker.
func NewTracker(issues *merr.Book) *Tracker {
	return &Tracker{
		Parser:     NewPSIParser(),
		Issues:     issues,
		pmtSeen:    map[uint16]bool{},
		pmtVersion: map[uint16]int{},
		pmtPIDs:    map[uint16]bool{},
	}
}

// Current returns the latest completed mapping (nil before one exists).
func (t *Tracker) Current() *Mapping {
	if t.current == nil || !t.current.Complete {
		return nil
	}
	return t.current
}

// OpenGap returns a copy of the currently unresolved mapping gap, if any.
func (t *Tracker) OpenGap() *MappingGap {
	if t.gap == nil {
		return nil
	}
	g := *t.gap
	return &g
}

// Handle feeds a packet through reassembly and PAT/PMT tracking. Only PID 0
// (PAT) and PMT PIDs announced by a valid PAT are interpreted as PSI, so PES
// packets on elementary PIDs are never mistaken for sections.
func (t *Tracker) Handle(pkt *Packet) {
	if pkt == nil {
		return
	}
	isPAT := pkt.PID == 0x0000
	isPMT := t.pmtPIDs[pkt.PID]
	if !isPAT && !isPMT {
		return
	}
	for _, sec := range t.Parser.Sections(pkt) {
		switch sec[0] {
		case 0x00:
			if isPAT {
				t.handlePAT(sec, pkt)
			}
		case 0x02:
			if isPMT {
				t.handlePMT(sec, pkt)
			}
		}
	}
}

func (t *Tracker) openGap(reason string, offset int64, from, to int) {
	if t.gap != nil {
		t.gap.Reason = reason
		t.gap.ToVersion = to
		return
	}
	g := &MappingGap{Reason: reason, StartOffset: offset, StartTick: t.NowTick,
		FromVersion: from, ToVersion: to}
	t.gap = g
}

func (t *Tracker) closeGap(offset int64) {
	if t.gap == nil {
		return
	}
	t.gap.EndOffset, t.gap.EndTick, t.gap.Resolved = offset, t.NowTick, true
	t.Gaps = append(t.Gaps, *t.gap)
	t.gap = nil
}

func (t *Tracker) handlePAT(sec []byte, pkt *Packet) {
	pat := ParsePAT(sec)
	if !pat.CRCValid {
		t.Issues.Add(merr.Issue{Category: merr.CatPSICRC, PID: 0, Offset: pkt.Offset,
			InputStart: pkt.Offset, InputEnd: pkt.Offset + PacketSize,
			PacketIndex: pkt.Index, Message: "PAT CRC mismatch", Recovery: merr.StateOpen})
		return
	}
	v := int(pat.Version)
	if t.hasPAT && v != t.patVersion {
		t.Issues.Add(merr.Issue{Category: merr.CatPSIVersionSwitch, PID: 0, Offset: pkt.Offset,
			InputStart: pkt.Offset, InputEnd: pkt.Offset + PacketSize, PacketIndex: pkt.Index,
			ClockTime: int64(t.NowTick), Message: fmt.Sprintf("PAT version %d -> %d", t.patVersion, v),
			Recovery: merr.StateRecovered,
			Attrs:    map[string]string{"table": "PAT", "from": itoa(t.patVersion), "to": itoa(v)}})
		t.openGap("version_switch", pkt.Offset, t.patVersion, v)
	}
	t.hasPAT, t.patVersion = true, v
	for _, pid := range pat.Programs {
		t.pmtPIDs[pid] = true
	}
	m := &Mapping{PATVersion: v, Programs: pat.Programs, SinceTick: t.NowTick, SinceOffset: pkt.Offset}
	if pid, ok := pat.Programs[1]; ok {
		m.PMTPID = pid
	}
	if t.current != nil && t.current.Complete {
		m.PMTVersion = t.current.PMTVersion
	}
	t.current, t.hasMapping = m, true
}

func (t *Tracker) handlePMT(sec []byte, pkt *Packet) {
	pmt := ParsePMT(sec)
	if !pmt.CRCValid {
		t.Issues.Add(merr.Issue{Category: merr.CatPSICRC, PID: int(pkt.PID), Offset: pkt.Offset,
			InputStart: pkt.Offset, InputEnd: pkt.Offset + PacketSize, PacketIndex: pkt.Index,
			Message: "PMT CRC mismatch; mapping not activated", Recovery: merr.StateOpen,
			Attrs: map[string]string{"program": fmt.Sprint(pmt.ProgramNumber), "version": fmt.Sprint(pmt.Version)}})
		t.openGap("psi_crc", pkt.Offset, int(pmt.Version), int(pmt.Version))
		return
	}
	v := int(pmt.Version)
	oldV := t.pmtVersion[pkt.PID]
	if t.pmtSeen[pkt.PID] && oldV != v {
		t.Issues.Add(merr.Issue{Category: merr.CatPSIVersionSwitch, PID: int(pkt.PID), Offset: pkt.Offset,
			InputStart: pkt.Offset, InputEnd: pkt.Offset + PacketSize, PacketIndex: pkt.Index,
			ClockTime: int64(t.NowTick), Message: fmt.Sprintf("PMT version %d -> %d", oldV, v),
			Recovery: merr.StateRecovered,
			Attrs:    map[string]string{"table": "PMT", "from": itoa(oldV), "to": itoa(v)}})
	}
	t.pmtSeen[pkt.PID] = true
	t.pmtVersion[pkt.PID] = v
	m := t.current
	if m == nil {
		m = &Mapping{Programs: map[uint16]uint16{}}
	}
	m.PMTPID, m.PMTVersion, m.PCRPID = pkt.PID, v, pmt.PCRPID
	m.Complete, m.SinceTick, m.SinceOffset = true, t.NowTick, pkt.Offset
	m.StreamPIDs, m.VideoPID, m.AudioPID = nil, 0, 0
	for _, s := range pmt.Streams {
		m.StreamPIDs = append(m.StreamPIDs, s.PID)
		switch s.StreamType {
		case 0x1B, 0x24: // H.264 / H.265 video
			if m.VideoPID == 0 {
				m.VideoPID = s.PID
			}
		case 0x0F, 0x11: // AAC / MPEG audio
			if m.AudioPID == 0 {
				m.AudioPID = s.PID
			}
		}
	}
	t.current = m
	t.History = append(t.History, *m)
	t.closeGap(pkt.Offset + PacketSize)
}
