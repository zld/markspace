package ts

import (
	"fmt"

	"mediarun/internal/clock"
)

const (
	patPID     uint16 = 0x0000
	maxSection        = 1024
)

// StreamInfo describes one elementary stream mapped by a PMT.
type StreamInfo struct {
	PID     uint16
	Type    uint8
	Track   string // audio|video|unknown
	Epoch   int    // mapping epoch when this PID became current
	Version uint8  // PMT version that introduced the mapping
}

// PSIMap is the active PAT/PMT view at one point in the stream.
type PSIMap struct {
	Version  uint8
	AudioPID uint16
	VideoPID uint16
	Streams  map[uint16]StreamInfo
	PMTPID   uint16
}

// PSITracker reassembles PSI sections, detects version updates and guarantees
// packets parsed under an old mapping are never reassigned to a new track.
type PSITracker struct {
	sections map[uint16][]byte // per-PID reassembly buffer
	versions map[uint16]uint8  // last applied table version per PID
	epoch    int
	current  *PSIMap
	history  []PSIMap
}

// NewPSITracker returns an empty tracker.
func NewPSITracker() *PSITracker {
	return &PSITracker{
		sections: map[uint16][]byte{},
		versions: map[uint16]uint8{},
		current:  &PSIMap{Streams: map[uint16]StreamInfo{}},
	}
}

// Current returns the active mapping.
func (t *PSITracker) Current() PSIMap { return *t.current }

// Epoch returns the current mapping epoch (0 before the first complete map).
func (t *PSITracker) Epoch() int { return t.epoch }

// History returns every mapping that has ever been active.
func (t *PSITracker) History() []PSIMap {
	out := make([]PSIMap, len(t.history))
	copy(out, t.history)
	return out
}

// isPSIPacket reports whether a PID may carry PAT or PMT content. The current
// PMT PID, every historically active one and the well-known PAT PID pass.
func (t *PSITracker) isPSIPacket(pid uint16) bool {
	if pid == patPID || pid == t.current.PMTPID {
		return true
	}
	for _, m := range t.history {
		if pid == m.PMTPID {
			return true
		}
	}
	return false
}

// TrackOf resolves a PID against the active mapping; unknown PIDs stay unknown.
func (t *PSITracker) TrackOf(pid uint16) string {
	if s, ok := t.current.Streams[pid]; ok {
		return s.Track
	}
	return "unknown"
}

// Observe feeds a packet; when a complete new-version table is reassembled the
// mapping is rebuilt and a psi_update event is recorded. Returns true when a
// mapping update happened on this packet.
func (t *PSITracker) Observe(p *Packet, rec *clock.Recorder) bool {
	if !t.isPSIPacket(p.PID) {
		return false
	}
	if !p.PayloadUnitStart || len(p.Payload) == 0 {
		return false
	}
	off := 0
	if p.Payload[0] != 0 { // pointer_field
		off = int(p.Payload[0])
	}
	off++ // skip the pointer_field byte itself
	if off >= len(p.Payload) {
		return false
	}
	if id := p.Payload[off]; id != 0x00 && id != 0x02 {
		return false // only PAT (0x00) and PMT (0x02) tables are handled
	}
	section := t.sections[p.PID]
	section = append(section, p.Payload[off:]...)
	if len(section) < 3 {
		t.sections[p.PID] = section
		return false
	}
	sectionLen := int(section[1]&0x0f)<<8 | int(section[2])
	total := 3 + sectionLen
	if len(section) < total {
		if len(section) > maxSection {
			delete(t.sections, p.PID)
		}
		t.sections[p.PID] = section
		return false
	}
	delete(t.sections, p.PID)
	// Version/control byte is full-section offset 5 per ISO 13818-1:
	// 0 table_id, 1-2 section_length, 3-4 table id extension, 5 version/control.
	version := (section[5] >> 1) & 0x1f
	if v, seen := t.versions[p.PID]; seen && v == version {
		return false
	}
	if err := CRC32MPEG2(section[:total]); err != nil {
		rec.Add(clock.Event{Kind: "psi_crc", Severity: "error", PID: p.PID,
			Range: clock.ByteRange{Start: p.Offset, End: p.Offset + PacketSize}, Detail: err.Error()})
		return false
	}
	t.versions[p.PID] = version
	if section[0] == 0x00 {
		t.applyPAT(section, version, p, rec)
	} else {
		t.applyPMT(section, version, p, rec)
	}
	return true
}

func (t *PSITracker) applyPAT(section []byte, version uint8, p *Packet, rec *clock.Recorder) {
	// Program loop starts after the 8-byte table header and ends before CRC_32.
	sectionLen := int(section[1]&0x0f)<<8 | int(section[2])
	loopEnd := 3 + sectionLen - 4
	programs := section[8:loopEnd]
	for i := 0; i+4 <= len(programs); i += 4 {
		num := uint16(programs[i])<<8 | uint16(programs[i+1])
		pid := uint16(programs[i+2]&0x1f)<<8 | uint16(programs[i+3])
		if num != 0 { // program 0 carries the NIT PID
			t.current.PMTPID = pid
			rec.Add(clock.Event{Kind: "psi_update", Severity: "info", PID: p.PID,
				Range:  clock.ByteRange{Start: p.Offset, End: p.Offset + PacketSize},
				Detail: fmt.Sprintf("PAT v%d: program %d on PID 0x%x", version, num, pid)})
			return
		}
	}
}

func (t *PSITracker) applyPMT(section []byte, version uint8, p *Packet, rec *clock.Recorder) {
	m := PSIMap{Version: version, PMTPID: t.current.PMTPID, Streams: map[uint16]StreamInfo{}}
	// PCR_PID at full-section bytes 8-9, program_info_length at 10-11.
	infoLen := int(section[10]&0x0f)<<8 | int(section[11])
	cursor := 12 + infoLen
	sectionLen := int(section[1]&0x0f)<<8 | int(section[2])
	end := 3 + sectionLen - 4 // exclude CRC_32
	for cursor+5 <= end {
		stype := section[cursor]
		pid := uint16(section[cursor+1]&0x1f)<<8 | uint16(section[cursor+2])
		esLen := int(section[cursor+3]&0x0f)<<8 | int(section[cursor+4])
		track := classifyStream(stype)
		m.Streams[pid] = StreamInfo{PID: pid, Type: stype, Track: track,
			Epoch: t.epoch + 1, Version: version}
		if track == "audio" && m.AudioPID == 0 {
			m.AudioPID = pid
		}
		if track == "video" && m.VideoPID == 0 {
			m.VideoPID = pid
		}
		cursor += 5 + esLen
	}
	t.epoch++
	if len(t.current.Streams) > 0 {
		snapshot := *t.current
		snapshot.Streams = map[uint16]StreamInfo{}
		for pid, s := range t.current.Streams {
			snapshot.Streams[pid] = s
		}
		t.history = append(t.history, snapshot)
	}
	t.current = &m
	rec.Add(clock.Event{Kind: "psi_update", Severity: "info", PID: p.PID,
		Range: clock.ByteRange{Start: p.Offset, End: p.Offset + PacketSize},
		Detail: fmt.Sprintf("PMT v%d: audio=0x%x video=0x%x streams=%d",
			version, m.AudioPID, m.VideoPID, len(m.Streams))})
}

func classifyStream(t uint8) string {
	switch {
	case t == 0x0F || t == 0x11 || (t >= 0x81 && t <= 0x87) || t == 0x04:
		return "audio"
	case t >= 0x01 && t <= 0x06 || t == 0x10 || t == 0x1B || t == 0x24:
		return "video"
	default:
		return "unknown"
	}
}

// crc32MPEG2Table is the MPEG-2 PSI checksum polynomial 0x04C11DB7.
var crc32MPEG2Table = func() [256]uint32 {
	var tbl [256]uint32
	for i := uint32(0); i < 256; i++ {
		crc := i << 24
		for j := 0; j < 8; j++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
		tbl[i] = crc
	}
	return tbl
}()

// CRC32MPEG2 validates a full PSI section: the computed CRC over every byte
// including the trailing CRC_32 field must be zero.
func CRC32MPEG2(section []byte) error {
	if crc := CRC32MPEG2Value(section); crc != 0 {
		return fmt.Errorf("PSI CRC mismatch: computed 0x%08x", crc)
	}
	return nil
}

// CRC32MPEG2Value returns the running MPEG-2 CRC over the given bytes. Pass a
// section without its trailing 4 CRC bytes to obtain the value to append; pass
// the complete section (CRC included) to get zero when the section is valid.
func CRC32MPEG2Value(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc = (crc << 8) ^ crc32MPEG2Table[byte(crc>>24)^b]
	}
	return crc
}
