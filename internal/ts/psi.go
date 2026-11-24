package ts

import (
	"github.com/example/tsrepair/internal/model"
)

// StreamType identifies the kind of elementary stream carried by a PID.
type StreamType byte

const (
	StreamTypeVideo StreamType = 0x1B // H.264
	StreamTypeAudio StreamType = 0x0F // AAC ADTS
	StreamTypeOther StreamType = 0
)

// TrackKind classifies a mapped PID.
type TrackKind int

const (
	KindUnknown TrackKind = iota
	KindVideo
	KindAudio
)

func (k TrackKind) String() string {
	switch k {
	case KindVideo:
		return "video"
	case KindAudio:
		return "audio"
	default:
		return "unknown"
	}
}

// TrackMap is one PMT entry: an elementary PID and how to interpret it.
type TrackMap struct {
	PID        uint16
	Kind       TrackKind
	StreamType StreamType
}

// PSIMapping is the active PAT/PMT view of the stream.
type PSIMapping struct {
	Version       uint8
	PMTPID        uint16
	PCRPID        uint16
	Tracks        map[uint16]TrackMap
	EffectiveFrom int64 // byte offset of the packet that completed the mapping
}

// Lookup returns the track mapping for pid and whether it is known.
func (m *PSIMapping) Lookup(pid uint16) (TrackMap, bool) {
	if m == nil {
		return TrackMap{}, false
	}
	t, ok := m.Tracks[pid]
	return t, ok
}

// psiSection is a reassembled table section (pointer-field payload removed).
type psiSection struct {
	tableID  byte
	version  byte
	current  bool
	section  []byte // full section bytes starting at table_id
	pid      uint16
	offset   int64
}

// PSITracker reassembles PAT and PMT sections and applies version changes at
// the packet boundary where a new "current" table becomes complete.
type PSITracker struct {
	active     *PSIMapping
	assemblers map[uint16]*sectionAssembler
	// pending tables keyed by table id; applied when the version differs.
	patPending *psiSection
	pmtPending *psiSection
	versions   map[byte]uint8 // table id -> last applied version
	boundaries []VersionBoundary
}

// VersionBoundary records where one PSI version superseded another.
type VersionBoundary struct {
	TableID  byte
	Old, New uint8
	Offset   int64
}

// NewPSITracker returns an empty tracker.
func NewPSITracker() *PSITracker {
	return &PSITracker{
		assemblers: make(map[uint16]*sectionAssembler),
		versions:   make(map[byte]uint8),
	}
}

// Active returns the mapping currently in force (nil before the first table).
func (t *PSITracker) Active() *PSIMapping { return t.active }

// Boundaries returns the version switches observed, in order.
func (t *PSITracker) Boundaries() []VersionBoundary {
	out := make([]VersionBoundary, len(t.boundaries))
	copy(out, t.boundaries)
	return out
}

// Observe feeds one packet. PSI packets on PID 0 (PAT) and the advertised PMT
// PID are reassembled; completing a new-current section applies the version
// change immediately, so subsequent packets are mapped under the new table.
func (t *PSITracker) Observe(p *Packet) error {
	if !p.PayloadUnitStart && t.assemblers[p.PID] == nil {
		return nil
	}
	switch {
	case p.PID == PIDPAT:
	case t.active != nil && p.PID == t.active.PMTPID:
	default:
		// Unknown PID carrying table-like data: ignore until PAT tells us
		// where the PMT lives; mapping errors are raised by the caller.
		if p.PID != PIDPAT {
			return nil
		}
	}
	asm := t.assemblers[p.PID]
	if asm == nil {
		asm = &sectionAssembler{}
		t.assemblers[p.PID] = asm
	}
	sec, err := asm.push(p)
	if err != nil {
		return err
	}
	if sec == nil {
		return nil
	}
	sec.pid, sec.offset = p.PID, p.ByteOffset
	switch sec.tableID {
	case 0x00:
		return t.applyPAT(sec)
	case 0x02:
		return t.applyPMT(sec)
	default:
		return nil
	}
}

func (t *PSITracker) applyPAT(sec *psiSection) error {
	progNum, pmtPID, err := parsePAT(sec.section)
	if err != nil {
		return err.At(model.PIDNotApplicable, sec.offset)
	}
	_ = progNum
	if prev, ok := t.versions[0x00]; ok && prev == sec.version && t.active != nil {
		return nil // same version re-announced: no boundary
	}
	t.versions[0x00] = sec.version
	t.boundaries = append(t.boundaries, VersionBoundary{
		TableID: 0x00, Old: t.safeVersion(0x00), New: sec.version, Offset: sec.offset})
	next := &PSIMapping{
		Version: sec.version, PMTPID: pmtPID,
		Tracks: map[uint16]TrackMap{}, EffectiveFrom: sec.offset}
	if t.active != nil {
		// preserve already-known tracks until the matching PMT arrives
		next.Tracks = cloneTracks(t.active.Tracks)
		next.PCRPID = t.active.PCRPID
	}
	t.active = next
	return nil
}

func (t *PSITracker) applyPMT(sec *psiSection) error {
	pcrPID, tracks, err := parsePMT(sec.section)
	if err != nil {
		return err.At(model.PIDNotApplicable, sec.offset)
	}
	if t.active == nil {
		return model.New(model.StagePSI, model.SeverityFatal,
			"PMT observed before any PAT").At(sec.pid, sec.offset)
	}
	if prev, ok := t.versions[0x02]; ok && prev == sec.version {
		return nil
	}
	old := t.safeVersion(0x02)
	t.versions[0x02] = sec.version
	t.boundaries = append(t.boundaries, VersionBoundary{
		TableID: 0x02, Old: old, New: sec.version, Offset: sec.offset})
	// Version switch: replace the whole elementary-PID mapping atomically.
	t.active.Tracks = tracks
	t.active.PCRPID = pcrPID
	t.active.Version = sec.version
	t.active.EffectiveFrom = sec.offset
	return nil
}

func (t *PSITracker) safeVersion(table byte) uint8 {
	if v, ok := t.versions[table]; ok {
		return v
	}
	return 0
}

func cloneTracks(in map[uint16]TrackMap) map[uint16]TrackMap {
	out := make(map[uint16]TrackMap, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// sectionAssembler collects PSI payload bytes until section_length is reached.
type sectionAssembler struct {
	buf    []byte
	remain int // bytes still expected after the first payload
	started bool
}

// push returns a complete section or nil while more packets are needed.
func (a *sectionAssembler) push(p *Packet) (*psiSection, error) {
	payload := p.Payload
	if p.PayloadUnitStart {
		if len(payload) < 1 {
			return nil, model.New(model.StagePSI, model.SeverityFatal,
				"pointer field missing").Wrap(model.ErrPointerField).At(p.PID, p.ByteOffset)
		}
		pointer := int(payload[0])
		if pointer+1 > len(payload) {
			return nil, model.New(model.StagePSI, model.SeverityFatal,
				"pointer field extends beyond payload").
				Wrap(model.ErrPointerField).At(p.PID, p.ByteOffset)
		}
		a.buf = append(a.buf[:0], payload[1:1+pointer]...)
		rest := payload[1+pointer:]
		if len(rest) >= 3 {
			secLen := int(rest[1]&0x0F)<<8 | int(rest[2])
			a.remain = secLen + 3 - len(rest)
		}
		a.buf = append(a.buf, rest...)
		a.started = true
	} else {
		if !a.started {
			return nil, nil // continuation without a start: wait
		}
		take := len(payload)
		if take > a.remain {
			take = a.remain
		}
		a.buf = append(a.buf, payload[:take]...)
		a.remain -= take
	}
	if a.remain > 0 || len(a.buf) < 8 {
		return nil, nil
	}
	sec, derr := decodeSectionHeader(a.buf, p)
	if derr != nil {
		return nil, derr
	}
	return sec, nil
}

func decodeSectionHeader(buf []byte, p *Packet) (*psiSection, error) {
	if len(buf) < 8 {
		return nil, model.New(model.StagePSI, model.SeverityFatal,
			"section shorter than 8 bytes").At(p.PID, p.ByteOffset)
	}
	return &psiSection{
		tableID: buf[0],
		version: (buf[5] >> 1) & 0x1F,
		current: buf[5]&0x01 != 0,
		section: buf,
	}, nil
}

func parsePAT(sec []byte) (progNum uint16, pmtPID uint16, err *model.StreamError) {
	if sec[0] != 0x00 || len(sec) < 12 {
		return 0, 0, model.New(model.StagePSI, model.SeverityFatal, "malformed PAT")
	}
	progNum = uint16(sec[8])<<8 | uint16(sec[9])
	pmtPID = (uint16(sec[10]&0x1F) << 8) | uint16(sec[11])
	if pmtPID == 0 || pmtPID >= PIDNull {
		return 0, 0, model.New(model.StagePSI, model.SeverityFatal,
			"PAT carries no usable PMT PID").Wrap(model.ErrConflictingMap)
	}
	return progNum, pmtPID, nil
}

func parsePMT(sec []byte) (pcrPID uint16, tracks map[uint16]TrackMap, err *model.StreamError) {
	if sec[0] != 0x02 || len(sec) < 16 {
		return 0, nil, model.New(model.StagePSI, model.SeverityFatal, "malformed PMT")
	}
	pcrPID = (uint16(sec[8]&0x1F) << 8) | uint16(sec[9])
	piLen := (int(sec[10]&0x0F) << 8) | int(sec[11])
	pos := 12 + piLen
	end := len(sec) - 4 // exclude CRC_32
	tracks = map[uint16]TrackMap{}
	for pos+5 <= end {
		st := StreamType(sec[pos])
		pid := (uint16(sec[pos+1]&0x1F) << 8) | uint16(sec[pos+2])
		esInfoLen := (int(sec[pos+3]&0x0F) << 8) | int(sec[pos+4])
		pos += 5 + esInfoLen
		if _, dup := tracks[pid]; dup {
			return 0, nil, model.New(model.StagePSI, model.SeverityFatal,
				"duplicate elementary PID in PMT").Wrap(model.ErrConflictingMap)
		}
		tracks[pid] = TrackMap{PID: pid, StreamType: st, Kind: kindOf(st)}
	}
	return pcrPID, tracks, nil
}

func kindOf(st StreamType) TrackKind {
	switch st {
	case StreamTypeVideo:
		return KindVideo
	case StreamTypeAudio:
		return KindAudio
	default:
		return KindUnknown
	}
}
