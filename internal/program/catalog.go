// Package program keeps the evidence-based mapping state: the current
// PAT, the latest published PMT per PID, and the effective snapshot
// derived from them. A new snapshot only takes effect when the tables
// behind it are complete and CRC-valid.
package program

import (
	"fmt"
	"sort"

	"tsaudit/internal/psi"
)

// StreamInfo describes one elementary stream of an effective snapshot.
type StreamInfo struct {
	PID        uint16
	StreamType uint8
}

// ProgramState is one program's mapping inside a snapshot. PMTVersion
// is -1 while the referenced PMT has not been published yet.
type ProgramState struct {
	Program    uint16
	PMTPID     uint16
	PMTVersion int
	Streams    []StreamInfo
}

// Snapshot is the effective program mapping at a point in the stream.
type Snapshot struct {
	PATVersion int
	Programs   []ProgramState
}

type pmtTable struct {
	version int
	streams []StreamInfo
}

type pmtID struct {
	pid     uint16
	version int
}

// Catalog tracks published tables and version-change counters.
type Catalog struct {
	patVersion        int
	PATVersionChanges int
	PMTVersionChanges map[uint16]int // per program number
	programs          []uint16       // sorted, from current PAT
	pmtPIDOf          map[uint16]uint16
	pmts              map[uint16]*pmtTable
	lastPMT           map[uint16]pmtID // per program, last effective PMT
	effective         *Snapshot
}

// NewCatalog builds an empty catalog.
func NewCatalog() *Catalog {
	return &Catalog{
		patVersion:        -1,
		PMTVersionChanges: map[uint16]int{},
		pmtPIDOf:          map[uint16]uint16{},
		pmts:              map[uint16]*pmtTable{},
		lastPMT:           map[uint16]pmtID{},
	}
}

// ApplyPAT records a newly published PAT version. Republishing the same
// version has no effect.
func (c *Catalog) ApplyPAT(version uint8, entries []psi.PATEntry) {
	if c.patVersion == int(version) {
		return
	}
	if c.patVersion >= 0 {
		c.PATVersionChanges++
	}
	c.patVersion = int(version)
	c.programs = nil
	c.pmtPIDOf = map[uint16]uint16{}
	for _, e := range entries {
		c.programs = append(c.programs, e.Program)
		c.pmtPIDOf[e.Program] = e.PMTPID
	}
	sort.Slice(c.programs, func(i, j int) bool { return c.programs[i] < c.programs[j] })
}

// ApplyPMT records the latest published PMT on a PID.
func (c *Catalog) ApplyPMT(pmtPID uint16, version uint8, streams []psi.PMTStream) {
	si := make([]StreamInfo, 0, len(streams))
	for _, s := range streams {
		si = append(si, StreamInfo{PID: s.PID, StreamType: s.StreamType})
	}
	c.pmts[pmtPID] = &pmtTable{version: int(version), streams: si}
}

// Current returns the currently effective snapshot, or nil before the
// first PAT is published.
func (c *Catalog) Current() *Snapshot { return c.effective }

// PMTPIDs returns the PMT PIDs referenced by the current PAT.
func (c *Catalog) PMTPIDs() []uint16 {
	out := make([]uint16, 0, len(c.pmtPIDOf))
	for _, pid := range c.pmtPIDOf {
		out = append(out, pid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Effective recomputes the effective snapshot after a table publish and
// reports whether it differs from the currently effective one.
func (c *Catalog) Effective() (*Snapshot, bool) {
	if c.patVersion < 0 {
		return nil, false
	}
	snap := &Snapshot{PATVersion: c.patVersion}
	for _, prog := range c.programs {
		ps := ProgramState{Program: prog, PMTPID: c.pmtPIDOf[prog], PMTVersion: -1}
		if t := c.pmts[ps.PMTPID]; t != nil {
			ps.PMTVersion = t.version
			ps.Streams = t.streams
		}
		snap.Programs = append(snap.Programs, ps)
	}
	if snapshotEqual(c.effective, snap) {
		return c.effective, false
	}
	for _, ps := range snap.Programs {
		if ps.PMTVersion < 0 {
			continue
		}
		id := pmtID{ps.PMTPID, ps.PMTVersion}
		if prev, ok := c.lastPMT[ps.Program]; ok && prev != id {
			c.PMTVersionChanges[ps.Program]++
		}
		c.lastPMT[ps.Program] = id
	}
	c.effective = snap
	return snap, true
}

func snapshotEqual(a, b *Snapshot) bool {
	if a == nil || b == nil || a.PATVersion != b.PATVersion || len(a.Programs) != len(b.Programs) {
		return false
	}
	for i := range a.Programs {
		pa, pb := a.Programs[i], b.Programs[i]
		if pa.Program != pb.Program || pa.PMTPID != pb.PMTPID || pa.PMTVersion != pb.PMTVersion {
			return false
		}
	}
	return true
}

// StreamTypeName names common stream types; unknown types stay numeric.
func StreamTypeName(t uint8) string {
	switch t {
	case 0x02:
		return "MPEG-2 video"
	case 0x03:
		return "MPEG-1 audio"
	case 0x04:
		return "MPEG-2 audio"
	case 0x0F:
		return "AAC"
	case 0x1B:
		return "H.264"
	case 0x24:
		return "H.265"
	}
	return fmt.Sprintf("unknown(0x%02x)", t)
}
