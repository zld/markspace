package ingest

import (
	"tsindex/internal/fault"
)

// StreamInfo describes one elementary stream inside a PMT.
type StreamInfo struct {
	StreamType uint8
	Codec      string // "h264", "hevc", "aac", "other"
	// Complete is false for codecs whose keyframes this system cannot
	// identify; their index entries are explicitly marked incomplete.
	Complete bool
}

// ProgramMapping is one program entry of a mapping epoch.
type ProgramMapping struct {
	ProgramNumber uint16
	PMTPID        uint16
	Version       uint8
	Streams       map[uint16]StreamInfo
}

// MappingEpoch is the PAT/PMT-derived mapping valid from an offset onward.
type MappingEpoch struct {
	ID       int64
	Offset   int64
	Programs map[uint16]*ProgramMapping
	// ChangedPrograms lists programs whose mapping differs from the previous
	// epoch; only those assets need invalidation.
	ChangedPrograms []uint16
}

// PSITracker reassembles PAT/PMT sections packet by packet, validates their
// CRC-32/MPEG-2 and version fields, and emits mapping epochs on changes.
type PSITracker struct {
	rec      *fault.Recorder
	buf      map[uint16][]byte
	versions map[uint16]uint8 // table PID -> last accepted version
	current  *MappingEpoch
	epochID  int64
	pmtPIDs  map[uint16]bool
}

// NewPSITracker returns an empty tracker.
func NewPSITracker(rec *fault.Recorder) *PSITracker {
	return &PSITracker{
		rec:      rec,
		buf:      make(map[uint16][]byte),
		versions: make(map[uint16]uint8),
		pmtPIDs:  map[uint16]bool{PIDPAT: true},
	}
}

// Current returns the most recent accepted mapping epoch (nil before PAT).
func (t *PSITracker) Current() *MappingEpoch { return t.current }

// Feed consumes one packet record. It returns the new epoch and true when a
// validated table change produced one; otherwise nil, false.
func (t *PSITracker) Feed(rec *Record) (*MappingEpoch, bool) {
	pid := rec.PID
	if pid != PIDPAT && !t.pmtPIDs[pid] {
		return nil, false
	}
	payload := rec.Payload()
	if payload == nil {
		return nil, false
	}
	if rec.PUSI {
		ptr := int(payload[0])
		if 1+ptr >= len(payload) {
			return nil, false
		}
		t.buf[pid] = append(t.buf[pid][:0], payload[1:1+ptr]...)
		payload = payload[1+ptr:]
	} else if _, ok := t.buf[pid]; !ok {
		return nil, false // continuation bytes before any section start
	}
	t.buf[pid] = append(t.buf[pid], payload...)
	section, ok := t.completeSection(pid)
	if !ok {
		return nil, false
	}
	return t.acceptSection(rec.Offset, pid, section)
}

func (t *PSITracker) completeSection(pid uint16) ([]byte, bool) {
	b := t.buf[pid]
	if len(b) < 3 {
		return nil, false
	}
	sectionLen := (int(b[1]&0x0F) << 8) | int(b[2])
	total := 3 + sectionLen
	if len(b) < total {
		return nil, false
	}
	// Copy out: the following append reuses the same backing array and would
	// otherwise overwrite the section head with the trailing bytes.
	section := append([]byte(nil), b[:total]...)
	t.buf[pid] = append(t.buf[pid][:0], b[total:]...)
	return section, true
}

func (t *PSITracker) acceptSection(offset int64, pid uint16, section []byte) (*MappingEpoch, bool) {
	tableID := section[0]
	if pid == PIDPAT && tableID != 0x00 {
		return nil, false
	}
	if pid != PIDPAT && tableID != 0x02 {
		return nil, false
	}
	if crc32MPEG2(section) != 0 {
		t.rec.Record(fault.Fault{Kind: fault.KindBadCRC, Offset: offset,
			Recoverable: true, Detail: "PSI section CRC-32 mismatch"})
		return nil, false
	}
	version := section[5] >> 1 & 0x1F
	if prev, ok := t.versions[pid]; ok && prev == version {
		return nil, false // unchanged repeat
	}
	t.versions[pid] = version
	t.rec.Record(fault.Fault{Kind: fault.KindPSIUpdate, Offset: offset,
		Recoverable: true, Detail: "PSI version update accepted"})

	if pid == PIDPAT {
		return t.applyPAT(offset, version, section)
	}
	return t.applyPMT(offset, pid, uint16(version), section)
}

func (t *PSITracker) applyPAT(offset int64, version uint8, section []byte) (*MappingEpoch, bool) {
	sectionLen := int(section[1]&0x0F)<<8 | int(section[2])
	epoch := t.cloneEpoch(offset)
	changed := map[uint16]bool{}
	for pos := 8; pos <= 3+sectionLen-4-4; pos += 4 {
		prog := uint16(section[pos])<<8 | uint16(section[pos+1])
		pmtPID := uint16(section[pos+2]&0x1F)<<8 | uint16(section[pos+3])
		if prog == 0 {
			continue // network PID
		}
		t.pmtPIDs[pmtPID] = true
		existing, ok := epoch.Programs[prog]
		if !ok {
			// First-time discovery populates the map but invalidates nothing.
			epoch.Programs[prog] = &ProgramMapping{ProgramNumber: prog, PMTPID: pmtPID,
				Version: version, Streams: map[uint16]StreamInfo{}}
		} else if existing.PMTPID != pmtPID {
			existing.PMTPID = pmtPID
			existing.Version = version
			changed[prog] = true
		}
	}
	if len(changed) == 0 && t.current != nil {
		return nil, false
	}
	for prog := range changed {
		epoch.ChangedPrograms = append(epoch.ChangedPrograms, prog)
	}
	t.current = epoch
	return epoch, true
}

func (t *PSITracker) applyPMT(offset int64, tablePID, version uint16, section []byte) (*MappingEpoch, bool) {
	prog := uint16(section[3])<<8 | uint16(section[4])
	epoch := t.cloneEpoch(offset)
	pm, ok := epoch.Programs[prog]
	if !ok {
		pm = &ProgramMapping{ProgramNumber: prog, PMTPID: tablePID, Streams: map[uint16]StreamInfo{}}
		epoch.Programs[prog] = pm
	}
	streams := map[uint16]StreamInfo{}
	pos := 12 + (int(section[10]&0x0F)<<8 | int(section[11]))
	sectionLen := int(section[1]&0x0F)<<8 | int(section[2])
	end := 3 + sectionLen - 4
	for pos+5 <= end {
		streamType := section[pos]
		esPID := uint16(section[pos+1]&0x1F)<<8 | uint16(section[pos+2])
		infoLen := int(section[pos+3]&0x0F)<<8 | int(section[pos+4])
		streams[esPID] = StreamInfo{StreamType: streamType,
			Codec: codecName(streamType), Complete: isSupportedVideo(streamType)}
		pos += 5 + infoLen
	}
	// First-time PMT discovery only fills the mapping the PAT advertised; it
	// invalidates nothing. A later version change that alters the streams does.
	preExisting := ok && len(pm.Streams) > 0
	changed := preExisting && !sameStreams(pm.Streams, streams)
	pm.Streams = streams
	pm.Version = uint8(version)
	pm.PMTPID = tablePID
	if changed {
		epoch.ChangedPrograms = append(epoch.ChangedPrograms, prog)
	}
	t.current = epoch
	return epoch, true
}

func sameStreams(a, b map[uint16]StreamInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for pid, s := range a {
		if b[pid] != s {
			return false
		}
	}
	return true
}

func (t *PSITracker) cloneEpoch(offset int64) *MappingEpoch {
	t.epochID++
	ep := &MappingEpoch{ID: t.epochID, Offset: offset, Programs: map[uint16]*ProgramMapping{}}
	if t.current != nil {
		ep.ChangedPrograms = append(ep.ChangedPrograms, t.current.ChangedPrograms...)
		for prog, m := range t.current.Programs {
			cp := &ProgramMapping{ProgramNumber: m.ProgramNumber, PMTPID: m.PMTPID,
				Version: m.Version, Streams: map[uint16]StreamInfo{}}
			for pid, s := range m.Streams {
				cp.Streams[pid] = s
			}
			ep.Programs[prog] = cp
		}
	}
	return ep
}

func isSupportedVideo(streamType uint8) bool {
	return streamType == 0x1B || streamType == 0x24 // H.264 / HEVC
}

func codecName(streamType uint8) string {
	switch streamType {
	case 0x1B:
		return "h264"
	case 0x24:
		return "hevc"
	case 0x0F, 0x11:
		return "aac"
	default:
		return "other"
	}
}

// crc32MPEG2 returns the residue of the MPEG-2 CRC (polynomial 0x04C11DB7,
// init 0xFFFFFFFF, no reflection, no final XOR). A valid section residues 0.
func crc32MPEG2(b []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, x := range b {
		crc ^= uint32(x) << 24
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
