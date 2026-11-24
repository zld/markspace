package ts

import (
	"encoding/binary"

	"medkernel/media"
)

// Private descriptor tags used by self-generated streams. Real-world streams
// would carry equivalent information in standard registration/audio
// descriptors; the kernel only promises to parse its own fixtures.
const (
	DescAudio uint8 = 0xC0 // payload: channels(1) + layout(1) + sampleRate(4 BE)
	DescColor uint8 = 0xC1 // payload: tagLen(1) + colorTag(string)
)

// TrackMeta is the normalized metadata for one elementary stream.
type TrackMeta struct {
	PID        uint16
	StreamType uint8
	Channels   uint8
	Layout     uint8 // channel layout code (see audio package)
	SampleRate uint32
	ColorTag   string
	Language   string
}

// TrackMap is one immutable PSI generation. A new version number creates a new
// map and a boundary; old and new channel/color metadata must never be mixed.
type TrackMap struct {
	Version uint8
	PMTPID  uint16
	Audio   map[uint16]TrackMeta
	Video   map[uint16]TrackMeta
}

// Boundary marks the packet index at which a PSI version changed.
type Boundary struct {
	Index      int64
	PID        uint16
	OldVersion uint8
	NewVersion uint8
}

type sectionBuf struct {
	data     []byte
	expected int // total section length incl. 3 trailing CRC bytes, 0 = idle
}

// PSIWatcher reassembles PSI sections across TS packets and emits track-map
// boundaries whenever a PAT/PMT version number changes.
type PSIWatcher struct {
	sections   map[uint16]*sectionBuf
	pmtPIDs    map[uint16]bool
	versions   map[uint16]uint8
	current    *TrackMap
	Boundaries []Boundary
	Events     []*media.MediaError
}

// NewPSIWatcher builds an empty watcher.
func NewPSIWatcher() *PSIWatcher {
	return &PSIWatcher{
		sections: make(map[uint16]*sectionBuf),
		pmtPIDs:  map[uint16]bool{PIDPAT: true},
		versions: make(map[uint16]uint8),
		current:  &TrackMap{Audio: map[uint16]TrackMeta{}, Video: map[uint16]TrackMeta{}},
	}
}

// Current returns the active track map (never nil; empty before the first PSI).
func (w *PSIWatcher) Current() *TrackMap { return w.current }

// Feed consumes one parsed packet. PUSI starts a new section via pointer_field;
// subsequent payloads extend it until the declared section length is reached.
func (w *PSIWatcher) Feed(p Packet) error {
	if !p.HasPayload || !w.pmtPIDs[p.PID] {
		return nil
	}
	buf := w.sections[p.PID]
	payload := p.Payload
	if p.PUSI {
		if len(payload) < 1 {
			return nil
		}
		ptr := int(payload[0])
		if 1+ptr >= len(payload) {
			return nil
		}
		payload = payload[1+ptr:]
		// A section can end before the packet payload; the remaining bytes are
		// 0xFF stuffing. Trim to the declared section length when available.
		if len(payload) >= 3 {
			seclen := int(binary.BigEndian.Uint16(payload[1:3]) & 0x0FFF)
			if total := 3 + seclen; total < len(payload) {
				payload = payload[:total]
			}
		}
		buf = &sectionBuf{data: append([]byte(nil), payload...)}
		w.sections[p.PID] = buf
	} else {
		if buf == nil || len(buf.data) == 0 {
			return nil // section start was lost; wait for the next PUSI
		}
		buf.data = append(buf.data, payload...)
		// Continuation packets have no length field at their start (the
		// section header lives only in the PUSI packet); trim stuffing using
		// the length declared at the head of the reassembled section.
		if len(buf.data) >= 3 {
			seclen := int(binary.BigEndian.Uint16(buf.data[1:3]) & 0x0FFF)
			if total := 3 + seclen; total < len(buf.data) {
				buf.data = buf.data[:total]
			}
		}
	}
	if !w.sectionComplete(buf) {
		if len(buf.data) > 4096 { // bounded reassembly; drop runaway section
			delete(w.sections, p.PID)
			w.Events = append(w.Events, media.New(media.ClassMetadata, "ts.psi",
				"section exceeds 4KiB reassembly cap").WithPID(p.PID).WithIndex(p.Index))
		}
		return nil
	}
	return w.parseSection(p.PID, p.Index, buf.data)
}

// sectionComplete reports whether the reassembled bytes hold a full section.
// Buffers contain section data only (the pointer_field was stripped in Feed).
// The match is exact: a single TS packet may pad a short section with 0xFF
// stuffing, and a section may also span several packets, so "at least this
// long" would both misfire on stuffing and fail to wait for continuation.
func (w *PSIWatcher) sectionComplete(b *sectionBuf) bool {
	if len(b.data) < 3 {
		return false
	}
	seclen := int(binary.BigEndian.Uint16(b.data[1:3]) & 0x0FFF)
	return len(b.data) == 3+seclen
}

func (w *PSIWatcher) parseSection(pid uint16, idx int64, sec []byte) error {
	delete(w.sections, pid)
	tableID := sec[0]
	version := uint8(sec[5] >> 1 & 0x1F)
	if prev, ok := w.versions[pid]; ok && prev != version {
		w.Boundaries = append(w.Boundaries, Boundary{
			Index: idx, PID: pid, OldVersion: prev, NewVersion: version,
		})
	}
	w.versions[pid] = version
	switch tableID {
	case 0x00: // PAT
		return w.parsePAT(version, sec)
	case 0x02: // PMT
		return w.parsePMT(version, sec)
	default:
		return nil
	}
}

func (w *PSIWatcher) parsePAT(version uint8, data []byte) error {
	seclen := int(binary.BigEndian.Uint16(data[1:3]) & 0x0FFF)
	end := 3 + seclen - 4 // section_length includes the 4-byte CRC32
	w.current.Version = version
	pos := 8
	for pos+4 <= end {
		prog := binary.BigEndian.Uint16(data[pos : pos+2])
		pmtPID := binary.BigEndian.Uint16(data[pos+2:pos+4]) & 0x1FFF
		pos += 4
		if prog != 0 { // program 0 is the network PID, not a PMT
			w.pmtPIDs[pmtPID] = true
			w.current.PMTPID = pmtPID
		}
	}
	return nil
}

func (w *PSIWatcher) parsePMT(version uint8, data []byte) error {
	seclen := int(binary.BigEndian.Uint16(data[1:3]) & 0x0FFF)
	end := 3 + seclen - 4
	next := &TrackMap{
		Version: version, PMTPID: w.current.PMTPID,
		Audio: map[uint16]TrackMeta{}, Video: map[uint16]TrackMeta{},
	}
	progInfoLen := int(binary.BigEndian.Uint16(data[10:12]) & 0x0FFF)
	pos := 12 + progInfoLen
	for pos+5 <= end {
		streamType := data[pos]
		esPID := binary.BigEndian.Uint16(data[pos+1:pos+3]) & 0x1FFF
		esInfoLen := int(binary.BigEndian.Uint16(data[pos+3:pos+5]) & 0x0FFF)
		pos += 5
		descEnd := pos + esInfoLen
		meta := TrackMeta{PID: esPID, StreamType: streamType}
		for pos+2 <= descEnd {
			tag, dlen := data[pos], int(data[pos+1])
			pos += 2
			if pos+dlen > descEnd {
				break
			}
			w.parseDescriptor(tag, data[pos:pos+dlen], &meta)
			pos += dlen
		}
		pos = descEnd
		if meta.Channels > 0 || meta.SampleRate > 0 {
			next.Audio[esPID] = meta
		} else {
			next.Video[esPID] = meta
		}
	}
	// A new generation replaces the whole map: no silent metadata mixing.
	w.current = next
	return nil
}

func (w *PSIWatcher) parseDescriptor(tag uint8, body []byte, meta *TrackMeta) {
	switch tag {
	case DescAudio:
		if len(body) < 6 {
			return
		}
		meta.Channels = body[0]
		meta.Layout = body[1]
		meta.SampleRate = binary.BigEndian.Uint32(body[2:6])
	case DescColor:
		if len(body) < 1 {
			return
		}
		n := int(body[0])
		if n > len(body)-1 {
			n = len(body) - 1
		}
		meta.ColorTag = string(body[1 : 1+n])
	}
}
