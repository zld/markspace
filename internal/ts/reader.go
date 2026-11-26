package ts

import (
	"errors"
	"io"

	merr "mediapack/internal/errors"
)

// PacketSize is the fixed MPEG-TS packet size.
const PacketSize = 188

// SyncByte is the MPEG-TS synchronisation byte.
const SyncByte byte = 0x47

// NullPID is the padding packet PID whose CC is not meaningful.
const NullPID uint16 = 0x1FFF

// ErrTruncated is returned when a final partial TS packet is encountered.
var ErrTruncated = errors.New("truncated TS packet")

// PESHeader carries the PTS/DTS fields parsed from a PES packet.
type PESHeader struct {
	StreamID  byte
	PTS       uint64
	DTS       uint64
	HasPTS    bool
	HasDTS    bool
	HeaderLen int
}

// Packet is one decoded 188-byte TS packet. Payload aliases Raw.
type Packet struct {
	Index  int64 // index of full packets from the start of the reader
	Offset int64 // absolute byte offset (BaseOffset + local offset)
	Raw    []byte

	SyncOK   bool
	TEI      bool
	PUSI     bool
	Priority bool
	PID      uint16

	Scrambling byte
	AFC        byte // adaptation-field-control bits
	CC         byte

	AdaptationLength int
	PCRFlag          bool
	PCRBase          uint64
	PCRExt           uint16
	RandomAccess     bool

	PayloadOffset int
	Payload       []byte
	PES           *PESHeader
}

// Reader streams fixed 188-byte packets from an io.Reader with bounded memory.
type Reader struct {
	r          io.Reader
	baseOffset int64
	local      int64
	index      int64

	// Issues, when non-nil, receives sync-byte and truncation anomalies.
	Issues *merr.Book
}

// NewReader wraps r. baseOffset is the absolute input offset of byte zero of r
// (use 0 for a whole file, interval.StartOffset for a section).
func NewReader(r io.Reader, baseOffset int64, issues *merr.Book) *Reader {
	return &Reader{r: r, baseOffset: baseOffset, Issues: issues}
}

// Next returns the next full packet. At clean EOF it returns io.EOF. A final
// partial packet is recorded as a truncated_packet issue and ErrTruncated.
func (rd *Reader) Next() (*Packet, error) {
	raw := make([]byte, PacketSize)
	n, err := io.ReadFull(rd.r, raw)
	offset := rd.baseOffset + rd.local
	if err == io.EOF && n == 0 {
		return nil, io.EOF
	}
	if err == io.ErrUnexpectedEOF {
		if rd.Issues != nil && n > 0 {
			rd.Issues.Add(merr.Issue{
				Category:    merr.CatTruncatedPacket,
				Message:     "partial TS packet at tail",
				InputStart:  offset,
				InputEnd:    offset + int64(n),
				Offset:      offset,
				PacketIndex: rd.index,
				Recovery:    merr.StateOpen,
				Attrs:       map[string]string{"bytes_present": itoa(n), "bytes_expected": "188"},
			})
		}
		rd.local += int64(n)
		return nil, ErrTruncated
	}
	if err != nil {
		return nil, err
	}

	p := parsePacket(raw, rd.index, offset)
	if !p.SyncOK && rd.Issues != nil {
		rd.Issues.Add(merr.Issue{
			Category:    merr.CatSyncByte,
			Message:     "invalid sync byte",
			InputStart:  offset,
			InputEnd:    offset + PacketSize,
			Offset:      offset,
			PacketIndex: rd.index,
			PID:         int(p.PID),
			Recovery:    merr.StateOpen,
			Attrs:       map[string]string{"found": byteHex(raw[0])},
		})
	}
	if p.PUSI && len(p.Payload) >= 3 && p.Payload[0] == 0x00 && p.Payload[1] == 0x00 && p.Payload[2] == 0x01 {
		p.PES = parsePES(p.Payload)
	}
	rd.local += PacketSize
	rd.index++
	return p, nil
}

func parsePacket(raw []byte, index, offset int64) *Packet {
	p := &Packet{Raw: raw, Index: index, Offset: offset, PayloadOffset: 4}
	p.SyncOK = raw[0] == SyncByte
	p.TEI = raw[1]&0x80 != 0
	p.PUSI = raw[1]&0x40 != 0
	p.Priority = raw[1]&0x20 != 0
	p.PID = uint16(raw[1]&0x1F)<<8 | uint16(raw[2])
	p.Scrambling = raw[3] >> 6
	p.AFC = (raw[3] >> 4) & 0x03
	p.CC = raw[3] & 0x0F

	pos := 4
	if p.AFC&0x02 != 0 && pos < len(raw) {
		alen := int(raw[pos])
		p.AdaptationLength = alen
		pos++ // consume length byte
		if alen > 0 && pos < len(raw) {
			flags := raw[pos]
			p.RandomAccess = flags&0x40 != 0
			if flags&0x10 != 0 && pos+6 < len(raw) {
				b := raw[pos+1 : pos+7]
				p.PCRFlag = true
				p.PCRBase = uint64(b[0])<<25 | uint64(b[1])<<17 | uint64(b[2])<<9 |
					uint64(b[3])<<1 | uint64(b[4])>>7
				p.PCRExt = uint16(b[4]&0x01)<<8 | uint16(b[5])
			}
		}
		pos = 4 + 1 + alen
		if pos > PacketSize {
			pos = PacketSize
		}
	}
	p.PayloadOffset = pos
	if p.AFC&0x01 != 0 && pos < PacketSize {
		p.Payload = raw[pos:]
	}
	return p
}

// CCEventKind classifies a continuity-counter observation.
type CCEventKind string

const (
	CCOK        CCEventKind = "ok"
	CCDuplicate CCEventKind = "duplicate"
	CCJump      CCEventKind = "jump"
)

// CCEvent describes one CC transition for a PID.
type CCEvent struct {
	PID                   uint16
	Kind                  CCEventKind
	From, To              byte
	Missing               int
	PrevIndex, CurIndex   int64
	PrevOffset, CurOffset int64
}

// Issue converts a jump/duplicate event into the shared anomaly model.
func (e CCEvent) Issue() merr.Issue {
	i := merr.Issue{
		PID:            int(e.PID),
		Offset:         e.CurOffset,
		InputStart:     e.PrevOffset,
		InputEnd:       e.CurOffset + PacketSize,
		PacketIndex:    e.CurIndex,
		GapFromCC:      int(e.From),
		GapToCC:        int(e.To),
		GapPacketStart: e.PrevIndex,
		GapPacketEnd:   e.CurIndex,
		GapOffsetStart: e.PrevOffset,
		GapOffsetEnd:   e.CurOffset,
		Recovery:       merr.StateOpen,
	}
	if e.Kind == CCDuplicate {
		i.Category = merr.CatCCDuplicate
		i.Message = "duplicate continuity counter"
	} else {
		i.Category = merr.CatCCJump
		i.Message = "continuity counter jump"
		i.GapPackets = e.Missing
	}
	return i
}

// CCState is the last observed state for one PID.
type CCState struct {
	Seen        bool
	CC          byte
	PacketIndex int64
	Offset      int64
}

// CCTracker tracks continuity counters per PID.
type CCTracker struct {
	State map[uint16]CCState
}

// NewCCTracker creates an empty tracker.
func NewCCTracker() *CCTracker { return &CCTracker{State: map[uint16]CCState{}} }

// Observe feeds one packet and returns the transition classification. Null
// packets (0x1FFF) are not tracked.
func (t *CCTracker) Observe(p *Packet) *CCEvent {
	if p == nil || p.PID == NullPID {
		return nil
	}
	cur := CCEvent{PID: p.PID, To: p.CC, CurIndex: p.Index, CurOffset: p.Offset, Kind: CCOK}
	prev, ok := t.State[p.PID]
	if !ok || !prev.Seen {
		t.State[p.PID] = CCState{Seen: true, CC: p.CC, PacketIndex: p.Index, Offset: p.Offset}
		return nil
	}
	cur.From = prev.CC
	cur.PrevIndex, cur.PrevOffset = prev.PacketIndex, prev.Offset
	switch {
	case p.CC == prev.CC:
		cur.Kind = CCDuplicate
	case p.CC != (prev.CC+1)&0x0F:
		cur.Kind = CCJump
		d := (int(p.CC) - int(prev.CC) + 16) % 16
		if d > 0 {
			cur.Missing = d - 1
		}
	}
	t.State[p.PID] = CCState{Seen: true, CC: p.CC, PacketIndex: p.Index, Offset: p.Offset}
	if cur.Kind == CCOK {
		return nil
	}
	return &cur
}

// ParseAll drains r into a packet slice. It is a convenience for fixtures and
// small intervals; streaming callers should use Reader.Next directly.
func ParseAll(r io.Reader, baseOffset int64, issues *merr.Book) ([]*Packet, error) {
	rd := NewReader(r, baseOffset, issues)
	var out []*Packet
	for {
		p, err := rd.Next()
		if err == io.EOF {
			return out, nil
		}
		if err == ErrTruncated {
			return out, ErrTruncated
		}
		if err != nil {
			return out, err
		}
		out = append(out, p)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func byteHex(x byte) string {
	const h = "0123456789abcdef"
	return "0x" + string([]byte{h[x>>4], h[x&0xF]})
}
