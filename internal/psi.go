package kernel

import "fmt"

// TrackKind identifies a normalized track's media family.
type TrackKind string

const (
	TrackAudio TrackKind = "audio"
	TrackVideo TrackKind = "video"
	TrackData  TrackKind = "data"
)

// PSIMap is the currently effective Program Map Table projection.
type PSIMap struct {
	PATVersion uint8                `json:"pat_version"`
	PMTVersion uint8                `json:"pmt_version"`
	PMTPID     uint16               `json:"pmt_pid"`
	PCRPID     uint16               `json:"pcr_pid"`
	TrackPIDs  map[uint16]TrackKind `json:"track_pids"`
}

// PSISection is the small subset of PAT/PMT syntax used by the fixture.
type PSISection struct {
	TableID           uint8
	TableIDExt        uint16
	Version           uint8
	CurrentNext       bool
	SectionNumber     uint8
	LastSectionNumber uint8
	Programs          map[uint16]uint16
	PCRPID            uint16
	Streams           map[uint16]uint8
}

type pendingPSI struct {
	version  uint8
	last     uint8
	sections map[uint8]*PSISection
}

// PSITracker retains the old map while a replacement version is incomplete.
type PSITracker struct {
	Current      *PSIMap
	PAT          map[uint16]uint16
	PATVersion   uint8
	PMTPID       uint16
	pendingPMT   *pendingPSI
	PausedPIDs   map[uint16]struct{}
	AppliedCount int
}

// NewPSITracker creates an empty PSI tracker.
func NewPSITracker() *PSITracker {
	return &PSITracker{PausedPIDs: map[uint16]struct{}{}}
}

// HandleTSPayload strips the optional pointer field and parses a section.
func (t *PSITracker) HandleTSPayload(pid uint16, payload []byte) (*PSIMap, error) {
	if len(payload) == 0 {
		return nil, &MediaError{Stage: "psi", Code: "empty_payload", PID: pid, Recoverable: true}
	}
	pointer := int(payload[0])
	if 1+pointer >= len(payload) {
		return nil, &MediaError{Stage: "psi", Code: "bad_pointer", PID: pid, Recoverable: false}
	}
	sec, err := ParseSection(payload[1+pointer:])
	if err != nil {
		return nil, &MediaError{Stage: "psi", Code: "invalid_section", PID: pid, Err: err, Recoverable: false}
	}
	return t.HandleSection(pid, sec)
}

// HandleSection applies a parsed PAT or PMT section.
func (t *PSITracker) HandleSection(pid uint16, sec *PSISection) (*PSIMap, error) {
	switch sec.TableID {
	case 0x00: // PAT
		t.PAT = sec.Programs
		t.PATVersion = sec.Version
		for _, pmtPID := range sec.Programs {
			t.PMTPID = pmtPID
		}
		return t.Current, nil
	case 0x02: // PMT
		if t.PMTPID != 0 && pid != t.PMTPID {
			return nil, &MediaError{Stage: "psi", Code: "wrong_pmt_pid", PID: pid, Recoverable: false}
		}
		t.PMTPID = pid
		return t.handlePMT(sec), nil
	default:
		return t.Current, nil
	}
}

func (t *PSITracker) handlePMT(sec *PSISection) *PSIMap {
	if !sec.CurrentNext {
		return t.Current // "next" table is not effective yet.
	}
	if t.Current != nil && sec.Version == t.Current.PMTVersion && t.pendingPMT == nil {
		return t.Current
	}

	if t.pendingPMT == nil || t.pendingPMT.version != sec.Version {
		t.pendingPMT = &pendingPSI{
			version:  sec.Version,
			last:     sec.LastSectionNumber,
			sections: map[uint8]*PSISection{},
		}
		t.PausedPIDs = map[uint16]struct{}{}
		if t.Current != nil {
			for pid := range t.Current.TrackPIDs {
				t.PausedPIDs[pid] = struct{}{}
			}
		}
	}
	t.pendingPMT.sections[sec.SectionNumber] = sec
	for pid := range sec.Streams {
		t.PausedPIDs[pid] = struct{}{}
	}

	if uint8(len(t.pendingPMT.sections)) != t.pendingPMT.last+1 {
		return t.Current // old map remains effective.
	}
	for i := uint8(0); i <= t.pendingPMT.last; i++ {
		if _, ok := t.pendingPMT.sections[i]; !ok {
			return t.Current
		}
	}

	next := &PSIMap{
		PATVersion: t.PATVersion,
		PMTVersion: sec.Version,
		PMTPID:     t.PMTPID,
		PCRPID:     sec.PCRPID,
		TrackPIDs:  map[uint16]TrackKind{},
	}
	for _, part := range t.pendingPMT.sections {
		next.PCRPID = part.PCRPID
		for pid, streamType := range part.Streams {
			next.TrackPIDs[pid] = kindForStreamType(streamType)
		}
	}
	t.Current = next
	t.pendingPMT = nil
	t.PausedPIDs = map[uint16]struct{}{}
	t.AppliedCount++
	return t.Current
}

// CuttingEnabled reports whether a PID's mapping is complete and stable.
func (t *PSITracker) CuttingEnabled(pid uint16) bool {
	if t.Current == nil {
		return false
	}
	if _, ok := t.Current.TrackPIDs[pid]; !ok {
		return false
	}
	_, paused := t.PausedPIDs[pid]
	return !paused
}

// Incomplete returns true while a replacement PMT is missing sections.
func (t *PSITracker) Incomplete() bool { return t.pendingPMT != nil }

// PausedPIDList returns paused PIDs for checkpoint/reporting.
func (t *PSITracker) PausedPIDList() []uint16 {
	out := make([]uint16, 0, len(t.PausedPIDs))
	for pid := range t.PausedPIDs {
		out = append(out, pid)
	}
	return out
}

func kindForStreamType(streamType uint8) TrackKind {
	switch streamType {
	case 0x03, 0x04, 0x0f, 0x11:
		return TrackAudio
	case 0x01, 0x02, 0x1b, 0x24:
		return TrackVideo
	default:
		return TrackData
	}
}

// ParseSection decodes only the PAT/PMT fields needed by the kernel.
func ParseSection(b []byte) (*PSISection, error) {
	if len(b) < 8 {
		return nil, fmt.Errorf("section too short: %d", len(b))
	}
	sectionLen := int(uint16(b[1]&0x0f)<<8 | uint16(b[2]))
	if sectionLen+3 > len(b) {
		return nil, fmt.Errorf("section length %d exceeds bytes %d", sectionLen, len(b))
	}
	sec := &PSISection{
		TableID:           b[0],
		TableIDExt:        uint16(b[3])<<8 | uint16(b[4]),
		Version:           (b[5] >> 1) & 0x1f,
		CurrentNext:       b[5]&0x01 == 1,
		SectionNumber:     b[6],
		LastSectionNumber: b[7],
		Programs:          map[uint16]uint16{},
		Streams:           map[uint16]uint8{},
	}
	end := 3 + sectionLen - 4 // exclude CRC
	if end > len(b) {
		end = len(b)
	}
	switch sec.TableID {
	case 0x00:
		for off := 8; off+4 <= end; off += 4 {
			program := uint16(b[off])<<8 | uint16(b[off+1])
			pmtPID := uint16(b[off+2]&0x1f)<<8 | uint16(b[off+3])
			sec.Programs[program] = pmtPID
		}
	case 0x02:
		if end < 12 {
			return nil, fmt.Errorf("PMT header too short")
		}
		sec.PCRPID = uint16(b[8]&0x1f)<<8 | uint16(b[9])
		infoLen := int(uint16(b[10]&0x0f)<<8 | uint16(b[11]))
		off := 12 + infoLen
		for off+5 <= end {
			streamType := b[off]
			pid := uint16(b[off+1]&0x1f)<<8 | uint16(b[off+2])
			esInfoLen := int(uint16(b[off+3]&0x0f)<<8 | uint16(b[off+4]))
			sec.Streams[pid] = streamType
			off += 5 + esInfoLen
		}
	}
	return sec, nil
}
