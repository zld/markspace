package psi

import (
	"errors"
	"fmt"
)

// Well-known PSI PIDs.
const (
	PIDPAT uint16 = 0x0000 // PAT always travels on PID 0
)

// StreamInfo is one elementary stream entry of a PMT.
type StreamInfo struct {
	StreamType uint8  // raw value is retained even when unknown to this tool
	ElementaryPID uint16
}

// PMT is the semantic content of one published Program Map Table version.
type PMT struct {
	ProgramNumber uint16
	Version       uint8
	PCRPID        uint16
	Streams       []StreamInfo
}

// PAT is the semantic content of one published Program Association Table.
type PAT struct {
	Version       uint8
	// Programs maps program_number to the PID carrying its PMT.
	Programs      map[uint16]uint16
}

// Publication is emitted once for every newly published, fully validated
// table version. Retransmissions of a known version do not produce one.
type Publication struct {
	PID     uint16
	TableID uint8
	Version uint8
	// Wrapped is true when the version counter went 31 -> 0.
	Wrapped bool
	// PAT or PMT is populated according to TableID.
	PAT *PAT
	PMT *PMT
}

// VersionStats summarises how the version of one table PID evolved.
type VersionStats struct {
	PID             uint16
	TableID         uint8
	PublishedCount  int   // number of distinct versions ever published
	VersionChanges  int   // transitions to a different published version
	Retransmissions int   // repeats of an already published complete table
	Wraps           int   // 31 -> 0 transitions
	CurrentVersion  int   // -1 when nothing has been published yet
}

type perPID struct {
	buf        []byte // section bytes accumulated since the last PUSI
	started    bool   // a section start (PUSI) has been seen
	gotSection bool   // a complete section was extracted, awaiting next PUSI
	stats      VersionStats
}

// Assembler reassembles PSI sections from packet payloads and maintains the
// published version of every PAT/PMT it observes. Invalid sections never
// overwrite a previously published table; they are recorded as candidates.
type Assembler struct {
	pids map[uint16]*perPID

	// partialSections collects the sections seen so far for a not-yet-complete
	// table, keyed by table-id-extension.
	partial map[uint16]map[uint8]*Section

	// OnPublication, when set, receives every new distinct table version.
	OnPublication func(Publication)

	// Publications retains the full ordered publication history.
	Publications []Publication
	// Corrupt retains every failed section attempt with diagnostic detail.
	Corrupt []SectionCandidate
}

// NewAssembler creates an empty section assembler.
func NewAssembler() *Assembler {
	return &Assembler{
		pids:     map[uint16]*perPID{},
		partial:  map[uint16]map[uint8]*Section{},
	}
}

// Stats returns a stable (PID-sorted) snapshot of per-PID version statistics.
func (a *Assembler) Stats() []VersionStats {
	keys := make([]uint16, 0, len(a.pids))
	for pid := range a.pids {
		keys = append(keys, pid)
	}
	for i := 1; i < len(keys); i++ { // simple insertion sort, inputs are tiny
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	out := make([]VersionStats, 0, len(keys))
	for _, pid := range keys {
		out = append(out, a.pids[pid].stats)
	}
	return out
}

// FeedPayload delivers one packet payload for a PSI PID. payloadUnitStart
// mirrors the transport header PUSI flag. Transport-error packets must be
// withheld by the caller.
func (a *Assembler) FeedPayload(pid uint16, payloadUnitStart bool, payload []byte) {
	if len(payload) == 0 {
		return
	}
	st := a.pids[pid]
	if st == nil {
		st = &perPID{stats: VersionStats{PID: pid, CurrentVersion: -1}}
		a.pids[pid] = st
	}

	if payloadUnitStart {
		if len(payload) < 1 {
			return
		}
		// A new section starts; any bytes left over from the previous section
		// could not form a complete frame and are reported as such.
		if st.started && !st.gotSection && len(st.buf) > 0 {
			a.recordIncomplete(pid, st.buf)
		}
		pointer := int(payload[0])
		st.buf = st.buf[:0]
		st.started = true
		st.gotSection = false
		if pointer == 0 {
			st.buf = append(st.buf, payload[1:]...)
		} else {
			// pointer_field skips pointer bytes of the previous section.
			if pointer+1 > len(payload) {
				a.recordMalformed(pid, nil, "pointer_field exceeds payload")
				st.started = false
				return
			}
			if pointer > 0 {
				a.recordIncomplete(pid, payload[1:1+pointer])
			}
			st.buf = append(st.buf, payload[1+pointer:]...)
		}
	} else if st.started {
		st.buf = append(st.buf, payload...)
	} else {
		// Continuation packet without a seen section start: ignore, it belongs
		// to a section that began before this tool joined the stream.
		return
	}
	a.tryExtract(pid, st)
}

// tryExtract pulls every complete section out of the accumulation buffer.
func (a *Assembler) tryExtract(pid uint16, st *perPID) {
	for {
		if len(st.buf) < 3 {
			return
		}
		length := int(st.buf[1]&0x0f)<<8 | int(st.buf[2])
		if length < minSectionLength || length > maxSectionLength {
			a.recordMalformed(pid, st.buf, fmt.Sprintf("section_length %d out of range", length))
			st.buf, st.started, st.gotSection = nil, false, false
			return
		}
		total := 3 + length
		if len(st.buf) < total {
			return // wait for more continuation packets
		}
		frame := st.buf[:total]
		section, err := ValidateSection(frame)
		if err != nil {
			a.recordError(pid, frame, err)
		} else {
			a.acceptSection(pid, section)
		}
		// Anything beyond the extracted frame is stray data; PAT/PMT use one
		// section per PUSI in practice, so discard it defensively.
		st.buf = st.buf[total:]
		st.gotSection = true
	}
}

// acceptSection handles one validated section, publishing only complete
// current tables and distinguishing version changes from retransmissions.
func (a *Assembler) acceptSection(pid uint16, s *Section) {
	st := a.pids[pid]
	if !s.CurrentNext {
		// Next tables describe a future configuration; do not publish yet.
		return
	}
	if int(s.SectionNumber) > int(s.LastSection) {
		a.recordMalformed(pid, s.Raw, "section_number exceeds last_section_number")
		return
	}

	ext := s.TableIDExtension
	if a.partial[ext] == nil {
		a.partial[ext] = map[uint8]*Section{}
	}
	a.partial[ext][s.SectionNumber] = s
	if len(a.partial[ext]) != int(s.LastSection)+1 {
		return // wait for the remaining sections of this multi-section table
	}
	sections := a.partial[ext]
	delete(a.partial, ext)

	version := s.Version
	if st.stats.CurrentVersion == int(version) {
		st.stats.Retransmissions++
		return // identical complete table seen again
	}

	pub := Publication{PID: pid, TableID: s.TableID, Version: version}
	switch s.TableID {
	case TableIDPAT:
		pat, err := parsePAT(sections)
		if err != nil {
			a.recordMalformed(pid, s.Raw, err.Error())
			return
		}
		pub.PAT = pat
	case TableIDPMT:
		pmt, err := parsePMT(sections)
		if err != nil {
			a.recordMalformed(pid, s.Raw, err.Error())
			return
		}
		pub.PMT = pmt
	default:
		a.recordMalformed(pid, s.Raw, "unsupported table id")
		return
	}

	if st.stats.PublishedCount > 0 {
		pub.Wrapped = st.stats.CurrentVersion == 31 && version == 0
		st.stats.VersionChanges++
		if pub.Wrapped {
			st.stats.Wraps++
		}
	}
	st.stats.PublishedCount++
	st.stats.CurrentVersion = int(version)
	st.stats.TableID = s.TableID
	a.Publications = append(a.Publications, pub)
	if a.OnPublication != nil {
		a.OnPublication(pub)
	}
}

func (a *Assembler) recordIncomplete(pid uint16, buf []byte) {
	a.Corrupt = append(a.Corrupt, SectionCandidate{
		PID: pid, TableID: tableIDOf(buf), Version: versionOf(buf),
		Reason: errors.New("psi: incomplete section at section boundary").Error(),
	})
}

func (a *Assembler) recordMalformed(pid uint16, buf []byte, reason string) {
	a.Corrupt = append(a.Corrupt, SectionCandidate{
		PID: pid, TableID: tableIDOf(buf), Version: versionOf(buf),
		Reason: ErrMalformedSection.Error() + ": " + reason,
	})
}

func (a *Assembler) recordError(pid uint16, buf []byte, err error) {
	a.Corrupt = append(a.Corrupt, SectionCandidate{
		PID: pid, TableID: tableIDOf(buf), Version: versionOf(buf), Reason: err.Error(),
	})
}

func tableIDOf(buf []byte) uint8 {
	if len(buf) == 0 {
		return 0xff
	}
	return buf[0]
}

func versionOf(buf []byte) int {
	if len(buf) < 6 {
		return -1
	}
	return int((buf[5] >> 1) & 0x1f)
}
