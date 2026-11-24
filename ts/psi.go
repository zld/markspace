package ts

import (
	"encoding/binary"
	"fmt"

	"mediad/internal/clock"
)

// crc32MPEG2Table is the polynomial 0x04C11DB7 table used by PSI sections.
var crc32MPEG2Table = func() [256]uint32 {
	var tbl [256]uint32
	for i := uint32(0); i < 256; i++ {
		crc := i << 24
		for j := 0; j < 8; j++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
		tbl[i] = crc
	}
	return tbl
}()

// CRC32MPEG2 computes the checksum with init 0xFFFFFFFF and no final XOR,
// exactly as required by ISO/IEC 13818-1 PSI sections.
func CRC32MPEG2(b []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, x := range b {
		crc = crc<<8 ^ crc32MPEG2Table[byte(crc>>24)^x]
	}
	return crc
}

// PATEntry maps one program number to the PMT PID carrying its definition.
type PATEntry struct {
	Program uint16
	PMTPID  uint16
}

// PMTStream is one elementary stream entry of a PMT.
type PMTStream struct {
	PID    uint16
	Type   uint8
	TrackID string // stable per-PID label used by analysis subsystems
}

// ProgramMap is the currently effective mapping derived from PAT + PMTs.
type ProgramMap struct {
	Version uint8
	// active becomes true after the first CRC-valid table is published.
	active   bool
	Programs map[uint16]PATEntry
	Streams  map[uint16][]PMTStream // keyed by program number
	// EffectivePIDs lists every elementary PID currently mapped.
	EffectivePIDs []uint16
}

// sectionBuf accumulates one PSI section across packets of a table PID.
type sectionBuf struct {
	data  []byte
	total int // declared 3+section_length; 0 until the header is seen
	done  bool
}

// PSITracker assembles sections and only publishes a new ProgramMap when the
// full section passes its CRC32 check and its version is newer than the one
// in force. Old versions never overwrite a newer mapping.
type PSITracker struct {
	buffers map[uint16]*sectionBuf
	current *ProgramMap
	// Errs records CRC failures and version-rollback attempts.
	Errs []clock.RangeError
}

// NewPSITracker returns an empty tracker.
func NewPSITracker() *PSITracker {
	return &PSITracker{buffers: map[uint16]*sectionBuf{}, current: &ProgramMap{Programs: map[uint16]PATEntry{}, Streams: map[uint16][]PMTStream{}}}
}

// Current returns the mapping in force (never nil).
func (t *PSITracker) Current() *ProgramMap { return t.current }

// Feed consumes one packet. PAT/PMT packets are reassembled; other PIDs are
// ignored. The returned bool reports whether the effective map changed.
func (t *PSITracker) Feed(p Packet) (bool, error) {
	if p.PID != PIDPAT && !t.isKnownPMT(p.PID) {
		return false, nil
	}
	if !p.HasPayload() || len(p.Payload) == 0 {
		return false, nil
	}
	buf := t.buffers[p.PID]
	payload := p.Payload
	if p.PUSI {
		if len(payload) < 4 || int(payload[0])+3 >= len(payload) {
			return false, nil
		}
		payload = payload[1+int(payload[0]):] // skip pointer field
		if len(payload) < 3 {
			return false, nil
		}
		total := 3 + int(binary.BigEndian.Uint16(payload[1:3])&0x0fff)
		if len(payload) >= total {
			// Whole section fits in the PUSI packet; trailing bytes are stuffing.
			return t.publish(p.PID, payload[:total], p.Offset)
		}
		// Section spans packets: keep exactly the present prefix and wait.
		buf = &sectionBuf{data: append([]byte{}, payload...), total: total}
		t.buffers[p.PID] = buf
		return false, nil
	}
	if buf == nil || buf.done {
		return false, nil // no section in progress; ignore stray packets
	}
	need := buf.total - len(buf.data)
	if need > len(payload) {
		need = len(payload)
	}
	buf.data = append(buf.data, payload[:need]...)
	if len(buf.data) < buf.total {
		return false, nil // still waiting for continuation
	}
	buf.done = true
	return t.publish(p.PID, buf.data, p.Offset)
}

func (t *PSITracker) isKnownPMT(pid uint16) bool {
	for _, e := range t.current.Programs {
		if e.PMTPID == pid {
			return true
		}
	}
	return false
}

func (t *PSITracker) publish(pid uint16, section []byte, offset int64) (bool, error) {
	if len(section) < 8 {
		return false, fmt.Errorf("ts: psi section on pid %d too short", pid)
	}
	// CRC32 covers every section byte except the 4-byte CRC field itself;
	// a correct section therefore checksums to zero when the CRC is included.
	want := binary.BigEndian.Uint32(section[len(section)-4:])
	if got := CRC32MPEG2(section[:len(section)-4]); got != want {
		t.Errs = append(t.Errs, clock.RangeError{
			Kind: clock.KindCRC, Start: offset, End: offset + int64(len(section)),
			Msg: fmt.Sprintf("pid %d CRC mismatch: %08x != %08x", pid, got, want),
		})
		return false, nil
	}
	version := section[5] >> 1 & 0x1f
	cur := t.current
	// active is true once any table has been published; only then can a
	// lower version number be a rollback rather than the first mapping.
	if cur.active && version < cur.Version {
		t.Errs = append(t.Errs, clock.RangeError{
			Kind: clock.KindPSIVersion, Start: offset, End: offset + int64(len(section)),
			Msg: fmt.Sprintf("pid %d version %d older than active %d", pid, version, cur.Version),
		})
		return false, nil
	}
	if pid == PIDPAT {
		return t.applyPAT(version, section), nil
	}
	return t.applyPMT(version, section), nil
}

func (t *PSITracker) applyPAT(version uint8, section []byte) bool {
	entries := map[uint16]PATEntry{}
	for p := 8; p+4 <= len(section)-4; p += 4 {
		program := binary.BigEndian.Uint16(section[p:])
		if program == 0 {
			continue // network PID entry
		}
		entries[program] = PATEntry{
			Program: program,
			PMTPID:  binary.BigEndian.Uint16(section[p+2:]) & 0x1fff,
		}
	}
	next := &ProgramMap{
		Version:  version,
		active:   true,
		Programs: entries,
		Streams:  map[uint16][]PMTStream{},
	}
	t.current = next
	return true
}

func (t *PSITracker) applyPMT(version uint8, section []byte) bool {
	// section[0]=table_id, [1..2]=section_length, [3..4]=program_number,
	// [8..9]=PCR PID, [10..11]=program info length, stream loop at 12.
	program := binary.BigEndian.Uint16(section[3:5])
	proglen := int(binary.BigEndian.Uint16(section[10:12]) & 0x0fff)
	p := 12 + proglen
	end := len(section) - 4
	var streams []PMTStream
	for p+5 <= end {
		streamType := section[p]
		esPID := binary.BigEndian.Uint16(section[p+1:]) & 0x1fff
		infoLen := int(binary.BigEndian.Uint16(section[p+3:]) & 0x0fff)
		streams = append(streams, PMTStream{
			PID: esPID, Type: streamType,
			TrackID: fmt.Sprintf("pid-%04x", esPID),
		})
		p += 5 + infoLen
	}
	cur := t.current
	cur.Version = version
	cur.active = true
	cur.Streams[program] = streams
	pidSet := map[uint16]struct{}{}
	for _, ss := range cur.Streams {
		for _, s := range ss {
			pidSet[s.PID] = struct{}{}
		}
	}
	cur.EffectivePIDs = cur.EffectivePIDs[:0]
	for esPID := range pidSet {
		cur.EffectivePIDs = append(cur.EffectivePIDs, esPID)
	}
	return true
}
