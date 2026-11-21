// Package program maintains the published PSI view of a transport stream:
// which programs exist, which PID carries each program's PMT, and which
// elementary streams each PMT version maps. Mapping intervals are kept so a
// PID reused across a remap is never silently merged into one stream.
package program

import (
	"sort"

	"github.com/example/tsaudit/internal/psi"
)

// MappingRecord is one stable (program, elementary PID) relationship with the
// packet-index interval over which it was the active mapping.
type MappingRecord struct {
	ProgramNumber uint16 `json:"program_number"`
	PMTPID        uint16 `json:"pmt_pid"`
	ElementaryPID uint16 `json:"elementary_pid"`
	StreamType    uint8  `json:"stream_type"`
	FirstVersion  uint8  `json:"first_version"`
	LastVersion   uint8  `json:"last_version"`
	// FirstPacket/LastPacket bound the input packets for which this mapping
	// was active; -1 marks an interval still open at end of stream.
	FirstPacket int64 `json:"first_packet"`
	LastPacket  int64 `json:"last_packet"`
}

// streamKey identifies one elementary PID inside one program.
type streamKey struct {
	program uint16
	pid     uint16
}

// interval is one active window of a mapping.
type interval struct {
	version     uint8
	streamType  uint8
	firstPacket int64
	lastPacket  int64
}

type programState struct {
	pmtPID  uint16
	streams map[uint16][]interval // elementary PID -> ordered intervals
}

// Map is the cumulative, version-aware program mapping of a stream.
type Map struct {
	// PATPrograms is the latest program_number -> PMT PID association.
	PATPrograms map[uint16]uint16
	programs    map[uint16]*programState
	// activePIDs maps an elementary PID to the programs currently referencing
	// it, so Lookup stays O(1) even during a remap.
	activePIDs map[uint16]map[uint16]bool
}

// New creates an empty program map.
func New() *Map {
	return &Map{
		PATPrograms: map[uint16]uint16{},
		programs:    map[uint16]*programState{},
		activePIDs:  map[uint16]map[uint16]bool{},
	}
}

// ApplyPAT records a newly published PAT.
func (m *Map) ApplyPAT(pat *psi.PAT) {
	// Replace rather than merge: programs absent from a new PAT are gone.
	m.PATPrograms = make(map[uint16]uint16, len(pat.Programs))
	for program, pmtPID := range pat.Programs {
		m.PATPrograms[program] = pmtPID
		st := m.programs[program]
		if st == nil {
			st = &programState{streams: map[uint16][]interval{}}
			m.programs[program] = st
		}
		st.pmtPID = pmtPID
	}
}

// ApplyPMT records a newly published PMT. packetIndex marks the packet at
// which the new version becomes active; previous open intervals close at
// packetIndex-1. The same PID appearing under multiple PMT versions yields
// separate intervals instead of one merged stream.
func (m *Map) ApplyPMT(pmt *psi.PMT, packetIndex int64) {
	st := m.programs[pmt.ProgramNumber]
	if st == nil {
		st = &programState{streams: map[uint16][]interval{}}
		m.programs[pmt.ProgramNumber] = st
	}

	// Close every open interval of this program at the version boundary.
	for pid, ivs := range st.streams {
		if ivs[len(ivs)-1].lastPacket == -1 {
			ivs[len(ivs)-1].lastPacket = packetIndex - 1
			st.streams[pid] = ivs
		}
	}

	seen := map[uint16]bool{}
	for _, stream := range pmt.Streams {
		seen[stream.ElementaryPID] = true
		ivs := st.streams[stream.ElementaryPID]
		if n := len(ivs); n > 0 && ivs[n-1].version == pmt.Version {
			continue // idempotent re-application of the same version
		}
		st.streams[stream.ElementaryPID] = append(ivs, interval{
			version:     pmt.Version,
			streamType:  stream.StreamType,
			firstPacket: packetIndex,
			lastPacket:  -1,
		})
		if m.activePIDs[stream.ElementaryPID] == nil {
			m.activePIDs[stream.ElementaryPID] = map[uint16]bool{}
		}
		m.activePIDs[stream.ElementaryPID][pmt.ProgramNumber] = true
	}

	// PIDs dropped by the new version stop being active.
	for pid, progs := range m.activePIDs {
		if !seen[pid] && progs[pmt.ProgramNumber] {
			delete(progs, pmt.ProgramNumber)
			if len(progs) == 0 {
				delete(m.activePIDs, pid)
			}
		}
	}
}

// CloseIntervals marks every still-open interval as ending at lastPacket.
func (m *Map) CloseIntervals(lastPacket int64) {
	for _, st := range m.programs {
		for pid, ivs := range st.streams {
			if ivs[len(ivs)-1].lastPacket == -1 {
				ivs[len(ivs)-1].lastPacket = lastPacket
				st.streams[pid] = ivs
			}
		}
	}
}

// IsActive reports whether pid is currently an elementary stream of program.
func (m *Map) IsActive(program, pid uint16) bool {
	return m.activePIDs[pid] != nil && m.activePIDs[pid][program]
}

// IsElementaryPID reports whether pid was ever mapped as an elementary stream.
func (m *Map) IsElementaryPID(pid uint16) bool {
	return len(m.activePIDs[pid]) > 0
}

// PMTPID returns the PMT PID assigned to a program, or false if unknown.
func (m *Map) PMTPID(program uint16) (uint16, bool) {
	st, ok := m.programs[program]
	if !ok {
		return 0, false
	}
	return st.pmtPID, true
}

// Records returns all mapping records in a stable order: program number,
// then elementary PID. Every interval of a remapped PID is a separate record.
func (m *Map) Records() []MappingRecord {
	var out []MappingRecord
	programs := make([]uint16, 0, len(m.programs))
	for p := range m.programs {
		programs = append(programs, p)
	}
	sort.Slice(programs, func(i, j int) bool { return programs[i] < programs[j] })

	for _, program := range programs {
		st := m.programs[program]
		pids := make([]uint16, 0, len(st.streams))
		for pid := range st.streams {
			pids = append(pids, pid)
		}
		sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
		for _, pid := range pids {
			for _, iv := range st.streams[pid] {
				out = append(out, MappingRecord{
					ProgramNumber: program,
					PMTPID:        st.pmtPID,
					ElementaryPID: pid,
					StreamType:    iv.streamType,
					FirstVersion:  iv.version,
					LastVersion:   iv.version,
					FirstPacket:   iv.firstPacket,
					LastPacket:    iv.lastPacket,
				})
			}
		}
	}
	return out
}

// ElementaryPIDs returns the sorted set of elementary PIDs ever mapped.
func (m *Map) ElementaryPIDs() []uint16 {
	seen := map[uint16]bool{}
	for _, st := range m.programs {
		for pid := range st.streams {
			seen[pid] = true
		}
	}
	out := make([]uint16, 0, len(seen))
	for pid := range seen {
		out = append(out, pid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
