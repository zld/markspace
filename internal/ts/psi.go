// Package ts: PSI section assembly and program mapping snapshots.
//
// A new PAT/PMT version becomes current only after every section of that
// version is present and every CRC-32/MPEG-2 check passes. While an update is
// in flight the last valid snapshot stays in force, and version numbers are
// compared modulo 32 so 31 -> 0 is a forward step rather than a rollback.
package ts

import (
	"fmt"
	"hash/crc32"
	"sort"
)

// Section pool bounds: a hostile or damaged stream must not grow memory
// without limit during table reassembly.
const (
	MaxSectionsPerTable = 512
	MaxTableBytes       = 256 << 10
	MaxStreamsPerPMT    = 256
)

// StreamType classifies an elementary stream declared by a PMT.
type StreamType string

const (
	StreamVideo StreamType = "video"
	StreamAudio StreamType = "audio"
	StreamOther StreamType = "other"
)

// Stream is one elementary stream mapping from a PMT.
type Stream struct {
	PID         uint16
	StreamType  uint8
	Kind        StreamType
	Descriptors []Descriptor
}

// Descriptor is a PMT descriptor tag with its raw body.
type Descriptor struct {
	Tag  uint8
	Data []byte
}

// Program identifies a program and its PCR PID.
type Program struct {
	Number  uint16
	PMTPID  uint16
	PCRPID  uint16
	Streams []Stream
}

// Snapshot is an immutable, CRC-validated program mapping. Its ID is a
// stable content hash so indexes and manifests can cite it.
type Snapshot struct {
	PATVersion  uint8
	PMTVersion  uint8
	TransportID uint16
	Programs    []Program
	ID          string
	PacketIndex int64
	Offset      int64
}

// CRC32MPEG computes the CRC-32/MPEG-2 used by PSI sections.
func CRC32MPEG(b []byte) uint32 {
	var crc uint32 = 0xFFFFFFFF
	for _, c := range b {
		crc ^= uint32(c) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// CRCValid reports whether a full section (header through the four CRC bytes)
// passes validation.
func CRCValid(section []byte) bool {
	if len(section) < 12 {
		return false
	}
	return CRC32MPEG(section[:len(section)-4]) == crcRead(section[len(section)-4:])
}

func crcRead(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// section assembler state for one PID.
type sectionBuf struct {
	partial       []byte
	partialTotal  int
	partialOffset int64
	partialPacket int64
	lastCont      uint8
	haveCont      bool
}

// PSI assembles PAT and PMT sections from packet payloads and publishes only
// fully validated snapshots.
type PSI struct {
	patPID  uint16
	pmtPIDs map[uint16]bool

	bufs map[uint16]*sectionBuf

	// In-flight candidate tables, keyed by table id+version extension.
	patCandidate *patCandidate
	pmtCandidate map[uint16]*pmtCandidate

	pat     *patTable
	pmts    map[uint16]*pmtTable
	current *Snapshot

	snaps []*Snapshot
	diags []Diagnostic
	stats PSIStats
}

// PSIStats counts section-level outcomes.
type PSIStats struct {
	Sections      int64
	CRCOk         int64
	CRCFail       int64
	Truncated     int64
	Discontinuity int64
	Duplicates    int64
	Snapshots     int64
	Ignored       int64
}

type patCandidate struct {
	version uint8
	count   int
	have    map[int][]byte // section number -> full validated section
}
type patTable struct {
	version     uint8
	transportID uint16
	programs    []ProgramRef
	complete    bool
}
type ProgramRef struct {
	Number uint16
	PMTPID uint16
}

type pmtCandidate struct {
	pmtPID  uint16
	version uint8
	count   int
	have    map[int][]byte
}
type pmtTable struct {
	version  uint8
	program  *Program
	complete bool
}

// NewPSI creates a PSI assembler. patPID is normally 0.
func NewPSI(patPID uint16) *PSI {
	return &PSI{
		patPID:       patPID,
		pmtPIDs:      make(map[uint16]bool),
		bufs:         make(map[uint16]*sectionBuf),
		pmtCandidate: make(map[uint16]*pmtCandidate),
		pmts:         make(map[uint16]*pmtTable),
	}
}

// Snapshot returns the latest fully validated mapping, or nil before the
// first PAT+PMT pair is complete.
func (p *PSI) Snapshot() *Snapshot { return p.current }

// Snapshots returns the ordered list of published mappings.
func (p *PSI) Snapshots() []*Snapshot { return p.snaps }

// Stats returns the counters collected while assembling sections.
func (p *PSI) Stats() PSIStats { return p.stats }

// Diagnostics returns continuity/section problems that affect PSI.
func (p *PSI) Diagnostics() []Diagnostic { return p.diags }

func (p *PSI) diag(d Diagnostic) { p.diags = append(p.diags, d) }

// Feed consumes one scanned packet. Packets flagged as duplicates are ignored
// because they legitimately repeat a previously assembled payload.
func (p *PSI) Feed(pkt *Packet) {
	if pkt.PID != p.patPID && !p.pmtPIDs[pkt.PID] {
		return
	}
	if pkt.Scrambling != 0 {
		return
	}
	if pkt.AFC != 1 && pkt.AFC != 3 {
		if pkt.PayloadStart {
			if b := p.bufs[pkt.PID]; b != nil {
				b.partial = nil
			}
		}
		return
	}
	if !pkt.HasPayload {
		return
	}
	if pkt.Duplicate {
		p.stats.Duplicates++
		return
	}
	sb := p.bufs[pkt.PID]
	if sb == nil {
		sb = &sectionBuf{}
		p.bufs[pkt.PID] = sb
	}
	if pkt.PayloadStart {
		if len(sb.partial) > 0 {
			// A new section starts while the previous one was still short.
			// No continuation packet completed it, so it was truncated; the
			// last validated snapshot stays in force.
			p.stats.Truncated++
			p.diag(Diagnostic{Kind: DiagMalformedPacket, PID: pkt.PID, Offset: sb.partialOffset, Packet: sb.partialPacket,
				Detail: fmt.Sprintf("section truncated: promised %d bytes, got %d before next start", sb.partialTotal, len(sb.partial))})
			sb.partial = nil
		}
		if sb.haveCont && pkt.Continuity != ((sb.lastCont+1)&0xF) {
			p.stats.Discontinuity++
			p.diag(Diagnostic{Kind: DiagContinuityJump, PID: pkt.PID, Offset: pkt.Offset, Packet: pkt.Index,
				PrevCC: sb.lastCont, CC: pkt.Continuity, Detail: "PSI reassembly dropped partial section"})
			sb.partial = nil
		}
		// pointer_field precedes the section in every PUSI payload, for
		// PAT and PMT alike. A nonzero pointer reserves bytes for the tail
		// of a section that started in a previous packet.
		if len(pkt.Payload) == 0 {
			sb.partial = nil
			return
		}
		ptr := int(pkt.Payload[0])
		if 1+ptr > len(pkt.Payload) {
			p.stats.Truncated++
			p.diag(Diagnostic{Kind: DiagMalformedPacket, PID: pkt.PID, Offset: pkt.Offset, Packet: pkt.Index,
				Detail: "pointer_field beyond payload"})
			sb.partial = nil
			return
		}
		p.appendBytes(pkt, sb, pkt.Payload[1+ptr:])
	} else {
		p.appendBytes(pkt, sb, pkt.Payload)
	}
	sb.lastCont = pkt.Continuity
	sb.haveCont = true
}

func (p *PSI) appendBytes(pkt *Packet, sb *sectionBuf, data []byte) {
	for len(data) > 0 {
		if len(sb.partial) < 3 {
			need := 3 - len(sb.partial)
			if need > len(data) {
				need = len(data)
			}
			sb.partial = append(sb.partial, data[:need]...)
			data = data[need:]
			if len(sb.partial) == 3 {
				total := 3 + int(uint16(sb.partial[1]&0x0F)<<8|uint16(sb.partial[2]))
				if total > MaxTableBytes || total < 4 {
					p.stats.Truncated++
					p.diag(Diagnostic{Kind: DiagMalformedPacket, PID: pkt.PID, Offset: pkt.Offset, Packet: pkt.Index,
						Detail: fmt.Sprintf("section length %d out of range", total)})
					sb.partial = nil
					return
				}
				sb.partial = append(sb.partial, make([]byte, 0, total-3)...)
				sb.partialTotal = total
				sb.partialOffset = pkt.Offset
				sb.partialPacket = pkt.Index
			}
		}
		total := 3 + int(uint16(sb.partial[1]&0x0F)<<8|uint16(sb.partial[2]))
		if len(sb.partial) < total {
			need := total - len(sb.partial)
			if need > len(data) {
				need = len(data)
			}
			sb.partial = append(sb.partial, data[:need]...)
			data = data[need:]
		}
		if len(sb.partial) == total {
			sec := append([]byte(nil), sb.partial...)
			sb.partial = nil
			p.handleSection(pkt, sec)
		}
	}
}

func (p *PSI) handleSection(pkt *Packet, sec []byte) {
	p.stats.Sections++
	if !CRCValid(sec) {
		p.stats.CRCFail++
		p.diag(Diagnostic{Kind: DiagMalformedPacket, PID: pkt.PID, Offset: pkt.Offset, Packet: pkt.Index,
			Detail: fmt.Sprintf("CRC mismatch on table_id=0x%02x", sec[0])})
		return
	}
	p.stats.CRCOk++
	switch sec[0] {
	case 0x00:
		p.acceptPAT(pkt, sec)
	case 0x02:
		p.acceptPMT(pkt, sec)
	default:
		p.stats.Ignored++
	}
}

// ---- PAT ----

func (p *PSI) acceptPAT(pkt *Packet, sec []byte) {
	if len(sec) < 12 {
		p.stats.Truncated++
		return
	}
	version := (sec[5] >> 1) & 0x1F
	curNext := sec[5]&1 != 0
	secNum := int(sec[6])
	last := int(sec[7])
	tid := uint16(sec[3])<<8 | uint16(sec[4])
	if last >= MaxSectionsPerTable || secNum > last {
		p.stats.Truncated++
		p.diag(Diagnostic{Kind: DiagMalformedPacket, PID: pkt.PID, Offset: pkt.Offset, Packet: pkt.Index,
			Detail: fmt.Sprintf("PAT section numbering %d/%d invalid", secNum, last)})
		return
	}
	// The long-form header is eight bytes: table_id, section_length,
	// transport_stream_id, version/current_next, section_number,
	// last_section_number. Program entries follow and are four bytes each.
	if len(sec)-4 < 8 || (len(sec)-4-8)%4 != 0 {
		p.stats.Truncated++
		p.diag(Diagnostic{Kind: DiagMalformedPacket, PID: pkt.PID, Offset: pkt.Offset, Packet: pkt.Index,
			Detail: "PAT program entries not a multiple of four bytes"})
		return
	}
	if p.patCandidate == nil || (p.patCandidate.version != version) {
		p.patCandidate = &patCandidate{version: version, count: last + 1, have: make(map[int][]byte)}
	}
	// Version wrap: 31 -> 0 arrives as a new candidate; a stale candidate is
	// superseded because only a fully present table is ever published.
	if _, dup := p.patCandidate.have[secNum]; dup && !curNext {
		return
	}
	p.patCandidate.have[secNum] = sec
	if len(p.patCandidate.have) < p.patCandidate.count {
		return
	}
	// Complete: assemble program references from all sections in order.
	var refs []ProgramRef
	for i := 0; i < p.patCandidate.count; i++ {
		s := p.patCandidate.have[i]
		for j := 8; j < len(s)-4; j += 4 {
			num := uint16(s[j])<<8 | uint16(s[j+1])
			pid := uint16(s[j+2]&0x1F)<<8 | uint16(s[j+3])
			if num == 0 {
				continue // network PID, not a program
			}
			refs = append(refs, ProgramRef{Number: num, PMTPID: pid})
		}
	}
	if len(refs) > MaxStreamsPerPMT {
		p.stats.Truncated++
		return
	}
	p.pat = &patTable{version: version, transportID: tid, programs: refs, complete: true}
	p.patCandidate = nil
	for _, r := range refs {
		p.pmtPIDs[r.PMTPID] = true
	}
	p.tryPublish(pkt)
}

// ---- PMT ----

func (p *PSI) acceptPMT(pkt *Packet, sec []byte) {
	if len(sec) < 16 {
		p.stats.Truncated++
		return
	}
	version := (sec[5] >> 1) & 0x1F
	secNum := int(sec[6])
	last := int(sec[7])
	progNum := uint16(sec[3])<<8 | uint16(sec[4])
	if last != 0 {
		p.stats.Truncated++
		p.diag(Diagnostic{Kind: DiagMalformedPacket, PID: pkt.PID, Offset: pkt.Offset, Packet: pkt.Index,
			Detail: "PMT must be a single section"})
		return
	}
	pcrPID := uint16(sec[8]&0x1F)<<8 | uint16(sec[9])
	infoLen := int(sec[10]&0x0F)<<8 | int(sec[11])
	body := sec[12 : len(sec)-4]
	if infoLen > len(body) {
		p.stats.Truncated++
		p.diag(Diagnostic{Kind: DiagMalformedPacket, PID: pkt.PID, Offset: pkt.Offset, Packet: pkt.Index,
			Detail: "PMT program_info_length exceeds section"})
		return
	}
	streams := make([]Stream, 0, 8)
	rest := body[infoLen:]
	for len(rest) >= 5 {
		st := Stream{
			StreamType: rest[0],
			PID:        uint16(rest[1]&0x1F)<<8 | uint16(rest[2]),
		}
		esLen := int(rest[3]&0x0F)<<8 | int(rest[4])
		if 5+esLen > len(rest) {
			p.stats.Truncated++
			p.diag(Diagnostic{Kind: DiagMalformedPacket, PID: pkt.PID, Offset: pkt.Offset, Packet: pkt.Index,
				Detail: "PMT ES_info_length exceeds section"})
			return
		}
		for d := rest[5 : 5+esLen]; len(d) >= 2 && 2+int(d[1]) <= len(d); d = d[2+int(d[1]):] {
			st.Descriptors = append(st.Descriptors, Descriptor{Tag: d[0], Data: append([]byte(nil), d[2:2+int(d[1])]...)})
		}
		st.Kind = classifyStream(st.StreamType)
		streams = append(streams, st)
		rest = rest[5+esLen:]
	}
	if len(streams) > MaxStreamsPerPMT {
		p.stats.Truncated++
		return
	}
	prog := &Program{Number: progNum, PMTPID: pkt.PID, PCRPID: pcrPID, Streams: streams}
	cand := p.pmtCandidate[pkt.PID]
	if cand == nil || cand.version != version || cand.pmtPID != pkt.PID {
		cand = &pmtCandidate{pmtPID: pkt.PID, version: version, count: 1, have: make(map[int][]byte)}
		p.pmtCandidate[pkt.PID] = cand
	}
	if _, dup := cand.have[secNum]; dup {
		return
	}
	cand.have[secNum] = sec
	if len(cand.have) < cand.count {
		return
	}
	p.pmts[pkt.PID] = &pmtTable{version: version, program: prog, complete: true}
	delete(p.pmtCandidate, pkt.PID)
	p.tryPublish(pkt)
}

// classifyStream maps a PMT stream_type to a coarse lane.
func classifyStream(t uint8) StreamType {
	switch t {
	case 0x01, 0x02, 0x1B, 0x24, 0x10, 0x42, 0xD1, 0xEA:
		return StreamVideo
	case 0x03, 0x04, 0x0F, 0x11, 0x81, 0x87, 0x1C:
		return StreamAudio
	default:
		return StreamOther
	}
}

func (p *PSI) tryPublish(pkt *Packet) {
	if p.pat == nil || !p.pat.complete {
		return
	}
	if len(p.pat.programs) == 0 {
		return
	}
	programs := make([]Program, 0, len(p.pat.programs))
	for _, ref := range p.pat.programs {
		pmt := p.pmts[ref.PMTPID]
		if pmt == nil || !pmt.complete || pmt.program.Number != ref.Number {
			return // wait for a matching, complete PMT for every program
		}
		cp := *pmt.program
		cp.Streams = append([]Stream(nil), pmt.program.Streams...)
		programs = append(programs, cp)
	}
	// A snapshot is published only when the PAT or PMT version actually
	// changes; identically-versioned duplicates are ignored.
	if p.current != nil && p.current.PATVersion == p.pat.version &&
		p.current.PMTVersion == p.pmts[p.pat.programs[0].PMTPID].version && sameMapping(p.current.Programs, programs) {
		p.stats.Duplicates++
		return
	}
	snap := &Snapshot{
		PATVersion:  p.pat.version,
		PMTVersion:  p.pmts[p.pat.programs[0].PMTPID].version,
		TransportID: p.pat.transportID,
		Programs:    programs,
		PacketIndex: pkt.Index,
		Offset:      pkt.Offset,
	}
	snap.ID = snapshotID(snap)
	p.current = snap
	p.snaps = append(p.snaps, snap)
	p.stats.Snapshots++
}

func sameMapping(a, b []Program) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Number != b[i].Number || a[i].PMTPID != b[i].PMTPID || a[i].PCRPID != b[i].PCRPID {
			return false
		}
		if len(a[i].Streams) != len(b[i].Streams) {
			return false
		}
		for j := range a[i].Streams {
			if a[i].Streams[j].PID != b[i].Streams[j].PID || a[i].Streams[j].StreamType != b[i].Streams[j].StreamType {
				return false
			}
		}
	}
	return true
}

// snapshotID is a deterministic digest of the mapping, stable across runs.
func snapshotID(s *Snapshot) string {
	h := crc32.NewIEEE()
	fmt.Fprintf(h, "pat=%d pmt=%d tsid=%d", s.PATVersion, s.PMTVersion, s.TransportID)
	for _, pr := range s.Programs {
		fmt.Fprintf(h, "|p=%d pmt=%d pcr=%d", pr.Number, pr.PMTPID, pr.PCRPID)
		for _, st := range pr.Streams {
			fmt.Fprintf(h, " s=%d t=%d", st.PID, st.StreamType)
		}
	}
	return fmt.Sprintf("%08x", h.Sum32())
}

// MappingSignature is a stable digest of the *track layout*: program numbers,
// PCR assignment, elementary PIDs and stream types. It deliberately excludes
// version numbers, so a PMT version bump that keeps the same layout yields the
// same signature: the mapping did not change, and an index may continue
// across it without risking a stale track reference.
func (s *Snapshot) MappingSignature() string {
	h := crc32.NewIEEE()
	for _, pr := range s.Programs {
		fmt.Fprintf(h, "p=%d pcr=%d|", pr.Number, pr.PCRPID)
		for _, st := range pr.Streams {
			fmt.Fprintf(h, "%d:%d ", st.PID, st.StreamType)
		}
	}
	return fmt.Sprintf("%08x", h.Sum32())
}

// StreamByPID finds a stream across all programs of the snapshot.
func (s *Snapshot) StreamByPID(pid uint16) (Stream, bool) {
	for _, pr := range s.Programs {
		for _, st := range pr.Streams {
			if st.PID == pid {
				return st, true
			}
		}
	}
	return Stream{}, false
}

// PRCPID returns the PCR PID of the first program, if any.
func (s *Snapshot) PRCPID() (uint16, bool) {
	if len(s.Programs) == 0 {
		return 0, false
	}
	return s.Programs[0].PCRPID, true
}

// Copy returns a deep copy usable outside the assembler's lifetime.
func (s *Snapshot) Copy() *Snapshot {
	if s == nil {
		return nil
	}
	cp := *s
	cp.Programs = make([]Program, len(s.Programs))
	for i, pr := range s.Programs {
		cp.Programs[i] = pr
		cp.Programs[i].Streams = append([]Stream(nil), pr.Streams...)
	}
	return &cp
}

// SortDiagnostics orders PSI diagnostics by offset for deterministic output.
func (p *PSI) SortDiagnostics() {
	sort.SliceStable(p.diags, func(i, j int) bool { return p.diags[i].Offset < p.diags[j].Offset })
}
