package ts

// PSI table identifiers and layout constants (ISO/IEC 13818-1).
const (
	tableIDPAT = 0x00
	tableIDPMT = 0x02

	pidPAT uint16 = 0x0000
	pidNil uint16 = 0x1FFF

	sectionSyntaxIndicator = 0x01
)

// StreamKind classifies an elementary stream.
type StreamKind uint8

const (
	StreamUnknown StreamKind = iota
	StreamAudio
	StreamVideo
)

// PSIEvent is emitted whenever PAT or PMT content (including version) changes.
type PSIEvent struct {
	Kind       string // "PAT" or "PMT"
	Offset     int64  // TS packet offset carrying the section
	Version    uint8
	TableIDExt uint16 // transport_stream_id (PAT) or program_number (PMT)
	AudioPIDs  []uint16
	AudioTypes map[uint16]uint8 // PID -> stream type
	PCRPID     uint16           // PMT only; 0x1FFF when unused
}

// PMTEntry describes one PMT version's audio elementary stream.
type PMTEntry struct {
	ProgramNumber uint16
	Version       uint8
	PCRPID        uint16
	AudioTypes    map[uint16]uint8
}

// IsAudioStreamType reports whether a PMT stream type carries audio.
func IsAudioStreamType(st uint8) bool {
	switch st {
	case 0x03, 0x04, // MPEG-1 / MPEG-2 audio
		0x0F,       // AAC ADTS
		0x11,       // AAC LATM
		0x80, 0x81, // ATSC AC-3 / E-AC-3
		0x87: // E-AC-3 low bitrate
		return true
	default:
		return false
	}
}

// PSITracker reassembles PSI sections per PID and reports version changes.
// Sections that arrive out of order or repeat unchanged are ignored.
type PSITracker struct {
	assembly map[uint16]*sectionAssembler
	patVer   uint8
	patSet   bool
	pmtVer   map[uint16]uint8 // program number -> last version
	pmtSet   map[uint16]bool
}

type sectionAssembler struct {
	buf  []byte
	need int
}

// NewPSITracker creates an empty tracker.
func NewPSITracker() *PSITracker {
	return &PSITracker{
		assembly: map[uint16]*sectionAssembler{},
		pmtVer:   map[uint16]uint8{},
		pmtSet:   map[uint16]bool{},
	}
}

// Push feeds one PSI-bearing TS packet payload. payloadUnitStart marks a new
// section beginning at pointer_field. It returns one event per completed
// section whose content or version changed.
func (t *PSITracker) Push(pid uint16, offset int64, payloadUnitStart bool, payload []byte) ([]*PSIEvent, error) {
	asm := t.assembly[pid]
	if asm == nil {
		asm = &sectionAssembler{}
		t.assembly[pid] = asm
	}
	var events []*PSIEvent
	consume := func(data []byte) error {
		asm.buf = append(asm.buf, data...)
		ev, done, err := t.flushIfComplete(pid, offset, asm)
		if err != nil {
			return err
		}
		if done && ev != nil {
			events = append(events, ev)
		}
		return nil
	}
	if payloadUnitStart {
		if len(payload) < 1 {
			return nil, errPSI(offset, "empty pointer field")
		}
		pointer := int(payload[0])
		if 1+pointer > len(payload) {
			return nil, errPSI(offset, "pointer field %d exceeds payload", pointer)
		}
		if pointer > 0 {
			if err := consume(payload[1 : 1+pointer]); err != nil {
				return nil, err
			}
		}
		asm.buf = asm.buf[:0] // new section starts here
		if err := consume(payload[1+pointer:]); err != nil {
			return nil, err
		}
	} else {
		if len(asm.buf) == 0 {
			return nil, errPSI(offset, "PSI continuation without section start")
		}
		if err := consume(payload); err != nil {
			return nil, err
		}
	}
	return events, nil
}

// flushIfComplete parses a section once enough bytes have accumulated.
func (t *PSITracker) flushIfComplete(pid uint16, offset int64, asm *sectionAssembler) (*PSIEvent, bool, error) {
	if asm.need == 0 {
		asm.need = t.sectionLength(asm.buf)
	}
	if asm.need <= 0 || len(asm.buf) < asm.need {
		return nil, false, nil
	}
	section := make([]byte, asm.need)
	copy(section, asm.buf[:asm.need])
	asm.buf = asm.buf[asm.need:]
	asm.need = 0
	ev, err := t.parse(pid, offset, section)
	return ev, true, err
}

// sectionLength returns total section length including the 3-byte header,
// or 0 when fewer than 3 bytes are buffered.
func (t *PSITracker) sectionLength(buf []byte) int {
	if len(buf) < 3 {
		return 0
	}
	return 3 + int((uint16(buf[1]&0x0F)<<8)|uint16(buf[2]))
}

func (t *PSITracker) parse(pid uint16, offset int64, section []byte) (*PSIEvent, error) {
	if len(section) < 8 {
		return nil, errPSI(offset, "section too short: %d bytes", len(section))
	}
	tableID := section[0]
	if section[1]&0x80 == 0 {
		return nil, errPSI(offset, "section_syntax_indicator clear on PID 0x%x", pid)
	}
	version := uint8((section[5] >> 1) & 0x1F)
	cur := uint16(section[3])<<8 | uint16(section[4])
	crc := uint32(section[len(section)-4])<<24 | uint32(section[len(section)-3])<<16 |
		uint32(section[len(section)-2])<<8 | uint32(section[len(section)-1])
	switch tableID {
	case tableIDPAT:
		if pid != pidPAT {
			return nil, errPSI(offset, "PAT table on unexpected PID 0x%x", pid)
		}
		return t.handlePAT(offset, version, cur, crc, section)
	case tableIDPMT:
		return t.handlePMT(offset, version, cur, crc, section)
	default:
		return nil, nil // other PSI tables are not tracked
	}
}

func (t *PSITracker) handlePAT(offset int64, version uint8, tsid uint16, crc uint32, section []byte) (*PSIEvent, error) {
	if err := checkCRC(section, crc, offset); err != nil {
		return nil, err
	}
	if t.patSet && t.patVer == version {
		return nil, nil // unchanged rebroadcast
	}
	// section body ends before the 4-byte CRC
	body := section[8 : len(section)-4]
	if len(body)%4 != 0 {
		return nil, errPSI(offset, "PAT program loop length %d not multiple of 4", len(body))
	}
	ev := &PSIEvent{Kind: "PAT", Offset: offset, Version: version, TableIDExt: tsid}
	for i := 0; i+4 <= len(body); i += 4 {
		prog := uint16(body[i])<<8 | uint16(body[i+1])
		pmtPID := uint16(body[i+2]&0x1F)<<8 | uint16(body[i+3])
		if prog != 0 && pmtPID != pidNil {
			ev.AudioPIDs = append(ev.AudioPIDs, pmtPID) // PMT PIDs until PMT parsed
		}
	}
	t.patSet, t.patVer = true, version
	return ev, nil
}

func (t *PSITracker) handlePMT(offset int64, version uint8, program uint16, crc uint32, section []byte) (*PSIEvent, error) {
	if err := checkCRC(section, crc, offset); err != nil {
		return nil, err
	}
	if t.pmtSet[program] && t.pmtVer[program] == version {
		return nil, nil
	}
	if len(section) < 12 {
		return nil, errPSI(offset, "PMT section too short")
	}
	pcrPID := uint16(section[8]&0x1F)<<8 | uint16(section[9])
	piLen := int(uint16(section[10]&0x0F)<<8 | uint16(section[11]))
	pos := 12 + piLen
	end := len(section) - 4 // exclude CRC
	if pos > end {
		return nil, errPSI(offset, "PMT program_info_length %d overruns section", piLen)
	}
	audioTypes := map[uint16]uint8{}
	var audioPIDs []uint16
	for pos+5 <= end {
		st := section[pos]
		esPID := uint16(section[pos+1]&0x1F)<<8 | uint16(section[pos+2])
		esInfoLen := int(uint16(section[pos+3]&0x0F)<<8 | uint16(section[pos+4]))
		pos += 5
		if pos+esInfoLen > end {
			return nil, errPSI(offset, "PMT ES descriptor overruns section")
		}
		if IsAudioStreamType(st) {
			if _, seen := audioTypes[esPID]; !seen {
				audioPIDs = append(audioPIDs, esPID)
			}
			audioTypes[esPID] = st
		}
		pos += esInfoLen
	}
	t.pmtSet[program] = true
	t.pmtVer[program] = version
	return &PSIEvent{
		Kind: "PMT", Offset: offset, Version: version, TableIDExt: program,
		AudioPIDs: audioPIDs, AudioTypes: audioTypes, PCRPID: pcrPID,
	}, nil
}

// LastPATVersion reports the latest PAT version seen.
func (t *PSITracker) LastPATVersion() (uint8, bool) { return t.patVer, t.patSet }

// PMTEntry returns the latest parsed PMT for a program.
func (t *PSITracker) PMTEntry(program uint16) (PMTEntry, bool) {
	if !t.pmtSet[program] {
		return PMTEntry{}, false
	}
	return PMTEntry{ProgramNumber: program, Version: t.pmtVer[program]}, true
}
