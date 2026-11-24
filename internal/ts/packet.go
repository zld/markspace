// Package ts parses standard 188-byte MPEG-TS packets and the agreed RTP
// encapsulation of them. No other encapsulation is guessed: anything that is
// neither raw 188-byte TS nor a valid RTP-wrapped TS stream is rejected with
// a classified model error.
package ts

import (
	"errors"

	"github.com/example/tsrepair/internal/model"
)

// PacketSize is the only accepted MPEG-TS packet length.
const PacketSize = 188

// SyncByte marks the start of every TS packet.
const SyncByte byte = 0x47

const (
	pidMask         uint16 = 0x1FFF
	flagPUSI        byte   = 0x40
	flagPriority    byte   = 0x20
	afcMask         byte   = 0x30
	afcShift               = 4
	ccMask          byte   = 0x0F
	adaptFlagPCR    byte   = 0x10
	adaptFlagRandom byte   = 0x40
	adaptFlagDisc   byte   = 0x80
)

// Well-known PIDs.
const (
	PIDPAT  uint16 = 0x0000
	PIDNull uint16 = 0x1FFF
)

// AdaptationField holds the parsed adaptation field header.
type AdaptationField struct {
	Length          byte
	Discontinuity   bool
	RandomAccess    bool
	PCRFlag         bool
	HasPCR          bool
	PCRBase         uint64 // 33-bit 90 kHz base, raw
	PCRExtension    uint16 // 9-bit 27 MHz extension, raw
	RawFlags        byte
	AdaptationBytes []byte // full adaptation field including length byte
}

// Packet is one validated 188-byte TS packet.
type Packet struct {
	Raw               []byte
	PID               uint16
	PayloadUnitStart  bool
	Priority          bool
	AdaptationControl byte
	ContinuityCounter byte
	Adaptation        *AdaptationField
	Payload           []byte
	ByteOffset        int64 // offset of the sync byte in the input stream
}

// HasPayload reports whether the adaptation field control allows a payload.
func (p *Packet) HasPayload() bool {
	return p.AdaptationControl&0x01 != 0
}

// HasAdaptation reports whether the packet carries an adaptation field.
func (p *Packet) HasAdaptation() bool {
	return p.AdaptationControl&0x02 != 0
}

// IsNull reports the null/stuffing PID 0x1FFF.
func (p *Packet) IsNull() bool { return p.PID == PIDNull }

// Parse validates one 188-byte packet and returns its parsed form. The
// returned slices alias the input buffer; callers must not mutate it.
func Parse(buf []byte, offset int64) (*Packet, error) {
	if len(buf) != PacketSize {
		return nil, model.New(model.StageParse, model.SeverityFatal,
			"buffer is not a full 188-byte TS packet").
			Wrap(model.ErrPacketLength).At(model.PIDNotApplicable, offset)
	}
	if buf[0] != SyncByte {
		return nil, model.New(model.StageParse, model.SeverityFatal,
			"missing 0x47 sync byte").
			Wrap(model.ErrSyncByte).At(model.PIDNotApplicable, offset)
	}
	p := &Packet{
		Raw:               buf,
		ByteOffset:        offset,
		PayloadUnitStart:  buf[1]&flagPUSI != 0,
		Priority:          buf[1]&flagPriority != 0,
		PID:               (uint16(buf[1]&0x1F) << 8) | uint16(buf[2])&pidMask,
		AdaptationControl: (buf[3] & afcMask) >> afcShift,
		ContinuityCounter: buf[3] & ccMask,
	}
	pos := 4
	if p.HasAdaptation() {
		af, err := parseAdaptation(buf, pos, offset, p.PID)
		if err != nil {
			return nil, err
		}
		p.Adaptation = af
		pos += int(af.Length) + 1
		if pos > PacketSize {
			return nil, model.New(model.StageParse, model.SeverityFatal,
				"adaptation field extends beyond packet").
				Wrap(model.ErrAdaptationLength).At(p.PID, offset)
		}
	}
	if p.HasPayload() {
		if pos > PacketSize {
			return nil, model.New(model.StageParse, model.SeverityFatal,
				"payload start beyond packet").At(p.PID, offset)
		}
		p.Payload = buf[pos:PacketSize]
	}
	return p, nil
}

func parseAdaptation(buf []byte, pos int, offset int64, pid uint16) (*AdaptationField, error) {
	length := int(buf[pos])
	if length == 0 {
		// length byte present but no field body: legal stuffing-only form.
		return &AdaptationField{Length: 0, AdaptationBytes: buf[pos : pos+1]}, nil
	}
	end := pos + 1 + length
	if end > PacketSize {
		return nil, model.New(model.StageParse, model.SeverityFatal,
			"adaptation field extends beyond packet").
			Wrap(model.ErrAdaptationLength).At(pid, offset)
	}
	af := &AdaptationField{
		Length:          buf[pos],
		RawFlags:        buf[pos+1],
		Discontinuity:   buf[pos+1]&adaptFlagDisc != 0,
		RandomAccess:    buf[pos+1]&adaptFlagRandom != 0,
		PCRFlag:         buf[pos+1]&adaptFlagPCR != 0,
		AdaptationBytes: buf[pos:end],
	}
	if af.PCRFlag {
		if length < 7 {
			return nil, model.New(model.StageParse, model.SeverityFatal,
				"PCR flag set but adaptation field shorter than 7 bytes").
				Wrap(model.ErrAdaptationLength).At(pid, offset)
		}
		b := buf[pos+2 : pos+8]
		base := uint64(b[0])<<25 | uint64(b[1])<<17 | uint64(b[2])<<9 |
			uint64(b[3])<<1 | uint64(b[4])>>7
		ext := uint16(b[4]&0x01)<<8 | uint16(b[5])
		af.HasPCR, af.PCRBase, af.PCRExtension = true, base, ext
	}
	return af, nil
}

// TruncatedRange describes a trailing fragment that cannot form a packet.
type TruncatedRange struct {
	Offset int64
	Length int
}

// SplitPackets walks a raw byte stream and yields every complete 188-byte
// packet. It resynchronizes at most by scanning for the next 0x47 aligned
// sync byte; a missing sync at a packet boundary is fatal. A non-empty
// trailing fragment is reported as a truncation range rather than guessed.
func SplitPackets(data []byte, yield func(*Packet) error) (*TruncatedRange, error) {
	i := 0
	if len(data) > 0 && data[0] != SyncByte {
		i = resync(data)
		if i < 0 {
			return nil, model.New(model.StageParse, model.SeverityFatal,
				"no 0x47 sync byte found in input").
				Wrap(model.ErrSyncByte).At(model.PIDNotApplicable, 0)
		}
	}
	seq := uint16(0)
	for ; i+PacketSize <= len(data); i += PacketSize {
		if data[i] != SyncByte {
			j := resync(data[i:])
			if j < 0 {
				return nil, model.New(model.StageParse, model.SeverityFatal,
					"lost packet synchronization").
					Wrap(model.ErrSyncByte).At(model.PIDNotApplicable, int64(i))
			}
			i += j
			if i+PacketSize > len(data) {
				break
			}
		}
		pkt, err := Parse(data[i:i+PacketSize], int64(i))
		if err != nil {
			return nil, err
		}
		if err := yield(pkt); err != nil {
			return nil, err
		}
		seq++
	}
	if i < len(data) {
		return &TruncatedRange{Offset: int64(i), Length: len(data) - i}, nil
	}
	return nil, nil
}

// resync returns the offset of the first sync byte in data, or -1.
func resync(data []byte) int {
	for i, b := range data {
		if b == SyncByte {
			return i
		}
	}
	return -1
}

// IsTruncated reports whether err is the classified truncation cause.
func IsTruncated(err error) bool {
	return errors.Is(err, model.ErrTruncatedPacket)
}
