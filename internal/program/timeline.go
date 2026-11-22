// Package program turns committed PAT/PMT tables into a timeline of program
// mappings. Every mapping is effective from a specific TS packet index until
// the packet where the next mapping commits, so downstream attribution of
// packets to elementary streams always uses the mapping in force at the time.
package program

import (
	"fmt"
	"sort"

	"tsrebuild/internal/psi"
)

// Interval is the half-open packet-index range [StartPacket, EndPacket) in
// which a mapping is effective. EndPacket is -1 while the interval is open.
type Interval struct {
	StartPacket int64
	EndPacket   int64
	Mapping     Mapping
}

// Mapping is one committed view of a program: where its PMT lives and which
// elementary streams it contains at a given PAT/PMT version pair.
type Mapping struct {
	ProgramNumber uint16
	PMTPID        uint16
	PCRPID        uint16
	Version       uint8 // PMT version; the PAT version is stored separately
	PATVersion    uint8
	Streams       []psi.ElementaryStream
}

// PIDMember describes one program role a PID plays inside a mapping.
type PIDMember struct {
	ProgramNumber uint16
	IsPMT         bool
	IsPCR         bool
	Stream        *psi.ElementaryStream // nil when the PID is the PMT PID only
}

// Timeline holds the mapping intervals of every program plus version-change
// counters. Tables that fail CRC or arrive incomplete never reach here, so
// every interval corresponds to a fully committed mapping.
type Timeline struct {
	tsID           uint16
	intervals      map[uint16][]*Interval // program number -> ordered intervals
	patChanges     int
	pmtChanges     map[uint16]int // program number -> committed PMT version changes
	lastPATVersion *uint8
}

// NewTimeline creates an empty timeline for a transport stream.
func NewTimeline(transportStreamID uint16) *Timeline {
	return &Timeline{
		tsID:       transportStreamID,
		intervals:  make(map[uint16][]*Interval),
		pmtChanges: make(map[uint16]int),
	}
}

// TransportStreamID returns the TSID the timeline was built for.
func (t *Timeline) TransportStreamID() uint16 { return t.tsID }

// ApplyPAT records a committed PAT. Version changes are counted only when the
// version differs from the previously committed one; repeated tables and
// duplicate sections are ignored by the caller and never reach this twice.
func (t *Timeline) ApplyPAT(pat *psi.PAT) {
	if t.lastPATVersion == nil || *t.lastPATVersion != pat.Version {
		if t.lastPATVersion != nil {
			t.patChanges++
		}
		v := pat.Version
		t.lastPATVersion = &v
	}
}

// ApplyMapping commits one program mapping effective at atPacket. If the
// program already has an open interval with the same PMT version nothing
// happens (repeated table); otherwise the previous interval is closed at
// atPacket and a new one opens.
func (t *Timeline) ApplyMapping(programNumber, pmtPID uint16, patVersion, pmtVersion uint8,
	pmt *psi.PMT, atPacket int64) (*Interval, error) {

	existing := t.intervals[programNumber]
	if n := len(existing); n > 0 {
		last := existing[n-1]
		if last.Mapping.Version == pmtVersion && last.Mapping.PATVersion == patVersion &&
			last.EndPacket == -1 {
			return last, nil // duplicate commit: no new interval, no new version
		}
		if pmtVersion != last.Mapping.Version {
			t.pmtChanges[programNumber]++
		}
		last.EndPacket = atPacket
	}

	streams := make([]psi.ElementaryStream, len(pmt.Streams))
	copy(streams, pmt.Streams)
	iv := &Interval{
		StartPacket: atPacket,
		EndPacket:   -1,
		Mapping: Mapping{
			ProgramNumber: programNumber,
			PMTPID:        pmtPID,
			PCRPID:        pmt.PCRPID,
			Version:       pmtVersion,
			PATVersion:    patVersion,
			Streams:       streams,
		},
	}
	t.intervals[programNumber] = append(existing, iv)
	return iv, nil
}

// PATVersionChanges reports how many times a new PAT version was committed.
func (t *Timeline) PATVersionChanges() int { return t.patChanges }

// PMTVersionChanges reports committed PMT version changes for one program.
func (t *Timeline) PMTVersionChanges(programNumber uint16) int {
	return t.pmtChanges[programNumber]
}

// Intervals returns a copy of the program's intervals in stream order.
func (t *Timeline) Intervals(programNumber uint16) []Interval {
	src := t.intervals[programNumber]
	out := make([]Interval, len(src))
	for i, iv := range src {
		out[i] = *iv
	}
	return out
}

// Programs returns the committed program numbers in ascending order.
func (t *Timeline) Programs() []uint16 {
	out := make([]uint16, 0, len(t.intervals))
	for p := range t.intervals {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// AtPacket returns the interval in force for the program at the given packet
// index, or an error if the program is unknown or no mapping is active yet.
func (t *Timeline) AtPacket(programNumber uint16, packetIndex int64) (*Interval, error) {
	for _, iv := range t.intervals[programNumber] {
		if packetIndex >= iv.StartPacket && (iv.EndPacket == -1 || packetIndex < iv.EndPacket) {
			return iv, nil
		}
	}
	return nil, fmt.Errorf("program %d: no mapping effective at packet %d", programNumber, packetIndex)
}

// LookupPID finds every role a PID plays in the program's given interval.
// PAT packets (PID 0) are global and intentionally not attributed here.
func LookupPID(m Mapping, pid uint16) []PIDMember {
	var out []PIDMember
	if pid == m.PMTPID {
		out = append(out, PIDMember{ProgramNumber: m.ProgramNumber, IsPMT: true, IsPCR: pid == m.PCRPID})
	}
	for i := range m.Streams {
		s := &m.Streams[i]
		if s.PID == pid {
			member := PIDMember{ProgramNumber: m.ProgramNumber, IsPCR: pid == m.PCRPID, Stream: s}
			out = append(out, member)
		}
	}
	return out
}
