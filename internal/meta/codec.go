package meta

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// Emsg is a normalized ISO emsg event (ISO/IEC 23009-1 5.10.3.3). Version 1
// absolute presentation time is always used internally.
type Emsg struct {
	SchemeIDURI   string
	Value         string
	Timescale     uint32
	Presentation  uint64 // in Timescale units
	EventDuration uint32
	ID            uint32
	Message       []byte
}

const (
	emsgMaxString = 128
	emsgMaxData   = 4096
)

// EncodeEmsg writes an emsg box payload (FullBox header included) in version 1.
func EncodeEmsg(e Emsg) ([]byte, error) {
	if len(e.SchemeIDURI) == 0 || len(e.SchemeIDURI) > emsgMaxString || len(e.Value) > emsgMaxString {
		return nil, fmt.Errorf("%w: emsg strings", ErrMetaFormat)
	}
	if e.Timescale == 0 || len(e.Message) > emsgMaxData {
		return nil, fmt.Errorf("%w: emsg fields", ErrMetaFormat)
	}
	buf := make([]byte, 0, 4+len(e.SchemeIDURI)+1+len(e.Value)+1+4+8+4+4+len(e.Message))
	buf = append(buf, 1, 0, 0, 0) // version 1, flags 0
	buf = append(buf, e.SchemeIDURI...)
	buf = append(buf, 0)
	buf = append(buf, e.Value...)
	buf = append(buf, 0)
	buf = binary.BigEndian.AppendUint32(buf, e.Timescale)
	buf = binary.BigEndian.AppendUint64(buf, e.Presentation)
	buf = binary.BigEndian.AppendUint32(buf, e.EventDuration)
	buf = binary.BigEndian.AppendUint32(buf, e.ID)
	buf = append(buf, e.Message...)
	return buf, nil
}

// ParseEmsgPayload decodes a version 1 emsg box payload.
func ParseEmsgPayload(b []byte) (Emsg, error) {
	var e Emsg
	if len(b) < 4 {
		return e, fmt.Errorf("%w: emsg header", ErrMetaFormat)
	}
	if b[0] != 1 {
		return e, fmt.Errorf("%w: emsg version %d (only 1 supported)", ErrMetaFormat, b[0])
	}
	if b[1]|b[2]|b[3] != 0 {
		return e, fmt.Errorf("%w: emsg flags", ErrMetaFormat)
	}
	off := 4
	readString := func() (string, error) {
		start := off
		for off < len(b) && b[off] != 0 {
			off++
			if off-start > emsgMaxString {
				return "", fmt.Errorf("%w: emsg string too long", ErrMetaLimit)
			}
		}
		if off >= len(b) {
			return "", fmt.Errorf("%w: emsg unterminated string", ErrMetaFormat)
		}
		s := string(b[start:off])
		off++
		return s, nil
	}
	var err error
	if e.SchemeIDURI, err = readString(); err != nil {
		return e, err
	}
	if e.Value, err = readString(); err != nil {
		return e, err
	}
	if len(b)-off < 20 {
		return e, fmt.Errorf("%w: emsg body truncated", ErrMetaFormat)
	}
	e.Timescale = binary.BigEndian.Uint32(b[off:])
	e.Presentation = binary.BigEndian.Uint64(b[off+4:])
	e.EventDuration = binary.BigEndian.Uint32(b[off+12:])
	e.ID = binary.BigEndian.Uint32(b[off+16:])
	off += 20
	e.Message = append([]byte(nil), b[off:]...)
	if len(e.Message) > emsgMaxData {
		return e, fmt.Errorf("%w: emsg message", ErrMetaLimit)
	}
	if e.Timescale == 0 {
		return e, fmt.Errorf("%w: emsg timescale 0", ErrMetaFormat)
	}
	return e, nil
}

// NewEmsgRecord wraps an emsg in a canonical metadata record. The record time
// is the event presentation time converted to milliseconds.
func NewEmsgRecord(trackID uint32, e Emsg) (Record, error) {
	payload, err := EncodeEmsg(e)
	if err != nil {
		return Record{}, err
	}
	return NewRecord(KindEmsg, trackID, e.Timescale, e.Presentation, uint64(e.EventDuration), schemeLabel(e.SchemeIDURI), payload)
}

// SEI is a normalized H.264 SEI user_data_unregistered message.
type SEI struct {
	UUID    string
	Payload []byte
}

const seiMaxPayload = 4096

// EncodeSEI builds the NAL unit (type 6) containing one user_data_unregistered
// payload with the fixed SEI UUID. Emulation prevention bytes are inserted.
func EncodeSEI(s SEI) ([]byte, error) {
	if len(s.UUID) != 16 || len(s.Payload) == 0 || len(s.Payload) > seiMaxPayload {
		return nil, fmt.Errorf("%w: sei fields", ErrMetaFormat)
	}
	body := make([]byte, 0, 2+len(s.UUID)+len(s.Payload))
	body = append(body, 5) // payloadType = user_data_unregistered
	size := len(s.UUID) + len(s.Payload)
	if size > 255 {
		return nil, fmt.Errorf("%w: sei size %d", ErrMetaLimit, size)
	}
	body = append(body, byte(size))
	body = append(body, s.UUID...)
	body = append(body, s.Payload...)
	body = append(body, 0x80) // rbsp_trailing_bits
	nal := append([]byte{0x06}, escapeRBSP(body)...)
	return nal, nil
}

// ParseSEI decodes a NAL unit produced by EncodeSEI and verifies the UUID.
func ParseSEI(nal []byte) (SEI, error) {
	var s SEI
	if len(nal) < 3 || nal[0]&0x1f != 6 {
		return s, fmt.Errorf("%w: not an sei nal", ErrMetaFormat)
	}
	body, err := unescapeRBSP(nal[1:])
	if err != nil {
		return s, err
	}
	if len(body) < 3 || body[0] != 5 {
		return s, fmt.Errorf("%w: sei payload type %d", ErrMetaFormat, body[0])
	}
	size := int(body[1])
	if len(body) < 2+size {
		return s, fmt.Errorf("%w: sei payload truncated", ErrMetaFormat)
	}
	data := body[2 : 2+size]
	if len(data) < 16 {
		return s, fmt.Errorf("%w: sei uuid truncated", ErrMetaFormat)
	}
	s.UUID = string(data[:16])
	if s.UUID != SEIUUID {
		return s, fmt.Errorf("%w: unexpected sei uuid %q", ErrMetaFormat, s.UUID)
	}
	s.Payload = append([]byte(nil), data[16:]...)
	// Strip the rbsp_trailing_bits byte that EncodeSEI appended.
	if n := len(s.Payload); n > 0 && s.Payload[n-1] == 0x80 {
		s.Payload = s.Payload[:n-1]
	}
	return s, nil
}

func escapeRBSP(b []byte) []byte {
	out := make([]byte, 0, len(b)+8)
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

func unescapeRBSP(b []byte) ([]byte, error) {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if i+2 < len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 3 {
			out = append(out, 0, 0)
			i += 2 // skip the emulation prevention byte; the next byte is data
			continue
		}
		out = append(out, b[i])
	}
	return out, nil
}

// NewSEIRecord wraps SEI in a canonical record at the given ms time.
func NewSEIRecord(trackID uint32, ticksMS uint64, s SEI) (Record, error) {
	payload, err := EncodeSEI(s)
	if err != nil {
		return Record{}, err
	}
	return NewRecord(KindSEI, trackID, 1000, ticksMS, 0, "sei", payload)
}

// PayloadDigest is the hex payload digest helper used in diagnostics.
func PayloadDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}
