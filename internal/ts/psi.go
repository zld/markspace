package ts

import "errors"

// PSI table identifiers relevant to a single-program transport stream.
const (
	tableIDPAT = 0x00
	tableIDPMT = 0x02
)

// Stream types recognized as video when scanning the PMT.
const (
	streamTypeH264    = 0x1B
	streamTypeH265    = 0x24
	streamTypeH265Alt = 0x25 // HEVC over private/legacy signaling
)

// ErrMalformedSection reports a PSI section whose length fields are invalid.
var ErrMalformedSection = errors.New("malformed PSI section")

// ProgramMap is the decoded result of one PMT: the elementary stream PIDs.
type ProgramMap struct {
	VideoPID  int
	AudioPIDs []int
}

// SectionAssembler collects PSI sections that may span multiple TS packets.
// State is bounded by one maximum-size section (~1 KiB), independent of
// stream length.
type SectionAssembler struct {
	pid      int
	buf      []byte
	expected int // total section bytes once the length field is known
	haveLen  bool
}

// NewSectionAssembler assembles sections carried on the given PID.
func NewSectionAssembler(pid int) *SectionAssembler {
	return &SectionAssembler{pid: pid}
}

// Feed appends one packet's payload. A PUSI packet begins a new section; its
// pointer_field is consumed per ISO 13818-1. Returns a complete section when
// one is available, otherwise nil while accumulation continues.
func (a *SectionAssembler) Feed(p *Packet) ([]byte, error) {
	if p.PID != a.pid || len(p.Payload) == 0 {
		return nil, nil
	}
	payload := p.Payload
	if p.PUSI {
		// New section starts; discard any unfinished previous section.
		a.buf = a.buf[:0]
		a.haveLen = false
		if len(payload) < 1 {
			return nil, ErrMalformedSection
		}
		pointer := int(payload[0])
		if pointer+1 > len(payload) {
			return nil, ErrMalformedSection
		}
		// Bytes before the pointer are padding from a previous section.
		payload = payload[1+pointer:]
	}
	a.buf = append(a.buf, payload...)
	if !a.haveLen && len(a.buf) >= 3 {
		// section_length covers bytes after its own field.
		secLen := int(uint16(a.buf[1]&0x0F)<<8 | uint16(a.buf[2]))
		if secLen < 4 || secLen > 1021 {
			return nil, ErrMalformedSection
		}
		a.expected = 3 + secLen
		a.haveLen = true
	}
	if a.haveLen && len(a.buf) >= a.expected {
		section := make([]byte, a.expected)
		copy(section, a.buf[:a.expected])
		// Retain any trailing bytes belonging to the next section; FFmpeg
		// output normally emits one section per PUSI, but stay defensive.
		a.buf = append(a.buf[:0], a.buf[a.expected:]...)
		a.haveLen = false
		return section, nil
	}
	return nil, nil
}

// ParsePAT extracts the (first) program number -> PMT PID mapping.
// Returns 0,0 when the section is not a PAT.
func ParsePAT(section []byte) (programNum, pmtPID int, err error) {
	if len(section) < 8 || section[0] != tableIDPAT {
		return 0, 0, nil
	}
	// Entries start after table_id(1) + header(2) + tsid(2) + version(1)
	// + section/last section numbers(2) = byte 8; CRC is 3 trailing bytes.
	for pos := 8; pos+4 <= len(section)-4; pos += 4 {
		num := int(uint16(section[pos])<<8 | uint16(section[pos+1]))
		pid := int(uint16(section[pos+2])&0x1F)<<8 | int(section[pos+3])
		if num != 0 { // program 0 is the network PID, not a PMT
			return num, pid, nil
		}
	}
	return 0, 0, nil
}

// ParsePMT decodes elementary stream entries and locates the video PID.
func ParsePMT(section []byte) (*ProgramMap, error) {
	if len(section) < 12 || section[0] != tableIDPMT {
		return nil, nil
	}
	progInfoLen := int(uint16(section[10])&0x0F)<<8 | int(section[11])
	pos := 12 + progInfoLen
	if pos > len(section)-4 {
		return nil, ErrMalformedSection
	}
	pm := &ProgramMap{VideoPID: -1}
	for pos+5 <= len(section)-4 {
		streamType := int(section[pos])
		pid := int(uint16(section[pos+1])&0x1F)<<8 | int(section[pos+2])
		esInfoLen := int(uint16(section[pos+3])&0x0F)<<8 | int(section[pos+4])
		pos += 5 + esInfoLen
		if streamType == streamTypeH264 || streamType == streamTypeH265 ||
			streamType == streamTypeH265Alt {
			if pm.VideoPID < 0 {
				pm.VideoPID = pid
			}
		} else if streamType == 0x0F || streamType == 0x11 || streamType == 0x81 {
			pm.AudioPIDs = append(pm.AudioPIDs, pid)
		}
	}
	if pm.VideoPID < 0 {
		return nil, errors.New("PMT contains no recognized video elementary stream")
	}
	return pm, nil
}
