package ts

import (
	"fmt"
	"sort"

	"mediad/internal/model"
)

// PSIEvent records a version change observed in the Program Association
// Table or a Program Map Table.
type PSIEvent struct {
	Kind          string // "pat" | "pmt"
	Offset        int64
	Version       uint8
	ProgramNumber uint16 // 0 for PAT events
}

// ProgramMap is one program's PMT view: PCR PID plus the elementary PIDs
// that belong to the program and their stream types.
type ProgramMap struct {
	ProgramNumber uint16
	Version       uint8
	PCRPID        uint16
	Streams       map[uint16]uint8 // elementary PID -> stream type
	FirstOffset   int64            // offset where this mapping became current
}

// PSITracker maintains PAT/PMT versions and the resulting PID mapping.
// Output stages consult IsMapped before writing; when a media PID has no
// mapping they pause and Blocker names the first missing dependency.
type PSITracker struct {
	patVersion   uint8
	patSeen      bool
	pmtVersion   map[uint16]uint8 // program number -> PMT version
	pmtPid       map[uint16]uint16 // program number -> PMT PID (from PAT)
	maps         map[uint16]*ProgramMap
	pidProgram   map[uint16]uint16 // media/PCR PID -> owning program number
	// Events lists every PAT/PMT version transition in stream order.
	Events []PSIEvent
}

// NewPSITracker creates an empty tracker.
func NewPSITracker() *PSITracker {
	return &PSITracker{
		pmtVersion: make(map[uint16]uint8),
		pmtPid:     make(map[uint16]uint16),
		maps:       make(map[uint16]*ProgramMap),
		pidProgram: make(map[uint16]uint16),
	}
}

// Observe inspects a packet and updates tables when it carries PAT or PMT
// data. A non-PSI packet, or a table whose current-next indicator is not yet
// "current", returns nil.
func (t *PSITracker) Observe(p *model.Packet) (*PSIEvent, error) {
	if !p.HasPayload || len(p.Payload) < 2 {
		return nil, nil
	}
	ptr := int(p.Payload[0])
	start := 1 + ptr
	if start+8 >= len(p.Payload) {
		return nil, nil
	}
	b := p.Payload
	tableID := b[start]
	sectionLen := int(uint16(b[start+1]&0x0F)<<8 | uint16(b[start+2]))
	extID := uint16(b[start+3])<<8 | uint16(b[start+4])
	version := (b[start+5] >> 1) & 0x1F
	currentNext := b[start+5] & 0x01
	if currentNext == 0 {
		return nil, nil // next table; wait until it becomes current
	}
	end := start + 3 + sectionLen
	if end > len(b) {
		return nil, &model.CorruptError{
			Kind: "psi", Offset: p.Offset, PID: p.PID,
			Detail: fmt.Sprintf("section length %d runs past packet payload (%d bytes)",
				sectionLen, len(b)-start),
		}
	}
	switch {
	case p.PID == model.PIDPAT && tableID == 0x00:
		return t.handlePAT(p, version, b[start+8:end-4])
	case tableID == 0x02:
		prog := extID
		return t.handlePMT(p, prog, version, b[start+8:end-4])
	default:
		return nil, nil
	}
}
func (t *PSITracker) handlePAT(p *model.Packet, version uint8, loop []byte) (*PSIEvent, error) {
	if t.patSeen && version == t.patVersion {
		return nil, nil
	}
	t.patSeen = true
	t.patVersion = version
	for len(loop) >= 4 {
		program := uint16(loop[0])<<8 | uint16(loop[1])
		pid := uint16(loop[2]&0x1F)<<8 | uint16(loop[3])
		if program != 0 { // program 0 carries the NIT PID, not a PMT
			t.pmtPid[program] = pid
		}
		loop = loop[4:]
	}
	ev := PSIEvent{Kind: "pat", Offset: p.Offset, Version: version}
	t.Events = append(t.Events, ev)
	return &ev, nil
}

func (t *PSITracker) handlePMT(p *model.Packet, program uint16, version uint8, body []byte) (*PSIEvent, error) {
	if prev, ok := t.pmtVersion[program]; ok && prev == version {
		return nil, nil
	}
	if len(body) < 4 {
		return nil, &model.CorruptError{Kind: "psi", Offset: p.Offset,
			PID: p.PID, Detail: "PMT section too short"}
	}
	m := &ProgramMap{
		ProgramNumber: program,
		Version:       version,
		PCRPID:        uint16(body[0]&0x1F)<<8 | uint16(body[1]),
		Streams:       make(map[uint16]uint8),
		FirstOffset:   p.Offset,
	}
	infoLen := int(uint16(body[2]&0x0F)<<8 | uint16(body[3]))
	pos := 4 + infoLen
	for pos+5 <= len(body) {
		streamType := body[pos]
		esPid := uint16(body[pos+1]&0x1F)<<8 | uint16(body[pos+2])
		esInfoLen := int(uint16(body[pos+3]&0x0F)<<8 | uint16(body[pos+4]))
		m.Streams[esPid] = streamType
		pos += 5 + esInfoLen
	}
	// A version switch replaces the program mapping atomically and
	// re-points every owned PID at the new program entry.
	if old, ok := t.maps[program]; ok {
		for pid := range old.Streams {
			delete(t.pidProgram, pid)
		}
		delete(t.pidProgram, old.PCRPID)
	}
	t.maps[program] = m
	t.pmtVersion[program] = version
	for pid := range m.Streams {
		t.pidProgram[pid] = program
	}
	t.pidProgram[m.PCRPID] = program
	ev := PSIEvent{Kind: "pmt", Offset: p.Offset, Version: version, ProgramNumber: program}
	t.Events = append(t.Events, ev)
	return &ev, nil
}

// IsMapped reports whether a media PID currently belongs to a known program.
func (t *PSITracker) IsMapped(pid uint16) bool {
	_, ok := t.pidProgram[pid]
	return ok
}

// Blocker returns a human-readable name for the first unmet dependency that
// prevents pid from being output, and false when the PID is fully mapped.
func (t *PSITracker) Blocker(pid uint16) (string, bool) {
	if t.IsMapped(pid) {
		return "", false
	}
	if !t.patSeen {
		return "pat", true
	}
	if _, ok := t.pidProgram[pid]; ok {
		return "", false
	}
	return fmt.Sprintf("pmt:pid%#x", pid), true
}

// MapForPID returns the program map owning pid, or nil when unmapped.
func (t *PSITracker) MapForPID(pid uint16) *ProgramMap {
	prog, ok := t.pidProgram[pid]
	if !ok {
		return nil
	}
	return t.maps[prog]
}

// PATVersion reports the last current PAT version seen.
func (t *PSITracker) PATVersion() (uint8, bool) { return t.patVersion, t.patSeen }

// VideoPIDs returns the elementary PIDs carrying the given stream type
// (e.g. 0x1B H.264 video) across all current program maps, sorted. It lets
// the segmenter follow PID changes introduced by a PMT version switch
// instead of pinning the PIDs seen at stream start.
func (t *PSITracker) VideoPIDs(streamType uint8) []uint16 {
	var out []uint16
	for _, m := range t.maps {
		for pid, st := range m.Streams {
			if st == streamType {
				out = append(out, pid)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// PCRPIDs returns all current PCR PIDs, sorted.
func (t *PSITracker) PCRPIDs() []uint16 {
	var out []uint16
	for _, m := range t.maps {
		out = append(out, m.PCRPID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
