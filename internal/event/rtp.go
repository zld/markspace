// Package event parses length-framed RTP event packets and expands 32-bit clocks.
package event

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/pion/rtp"

	"replayctl/internal/fault"
	"replayctl/internal/timebase"
)

const (
	// ClockRate is the RTP event clock rate required by the acceptance domain.
	ClockRate = timebase.EventRate
	maxPacket = 64 * 1024
	magic     = "RCE1"
)

// Event is one parsed event packet with its monotonically expanded timestamp.
type Event struct {
	ID           string
	Kind         string
	RawTimestamp uint32
	Tick         int64
	Sequence     uint16
	RawOffset    int64
}

// Unwrapper expands RTP's unsigned 32-bit timestamp using signed-half interval logic.
type Unwrapper struct {
	last        int64
	initialized bool
}

// Unwrap converts raw to a monotonic 64-bit tick. It is correct for consecutive
// observations whose forward/backward displacement is less than 2^31 ticks.
func (u *Unwrapper) Unwrap(raw uint32) int64 {
	if !u.initialized {
		u.last = int64(raw)
		u.initialized = true
		return u.last
	}
	delta := int32(uint32(raw) - uint32(u.last))
	u.last += int64(delta)
	return u.last
}

// EncodePayload creates the small stable event payload used by fixtures.
func EncodePayload(kind, id string) []byte {
	return []byte(fmt.Sprintf("%s\nkind=%s\nid=%s\n", magic, kind, id))
}

// ParsePayload decodes EncodePayload's format.
func ParsePayload(p []byte) (kind, id string, err error) {
	lines := bytes.Split(bytes.TrimRight(p, "\n"), []byte("\n"))
	if len(lines) != 3 || string(lines[0]) != magic {
		return "", "", fmt.Errorf("malformed event payload")
	}
	var ok bool
	kind, ok = strings.CutPrefix(string(lines[1]), "kind=")
	if !ok {
		return "", "", fmt.Errorf("missing kind")
	}
	id, ok = strings.CutPrefix(string(lines[2]), "id=")
	if !ok || kind == "" || id == "" {
		return "", "", fmt.Errorf("missing id")
	}
	return kind, id, nil
}

// MarshalPacket builds an RTP version-2 event packet.
func MarshalPacket(seq uint16, ts uint32, kind, id string) ([]byte, error) {
	pkt := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Marker:         true,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           0x52434531, // "RCE1"
		},
		Payload: EncodePayload(kind, id),
	}
	b, err := pkt.Marshal()
	if err != nil {
		return nil, err
	}
	return b, nil
}

// WriteRecord writes one length-prefixed RTP packet and returns its packet offset.
func WriteRecord(w io.Writer, packet []byte) (int64, error) {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(packet)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return 0, err
	}
	off := int64(4) // Callers writing a file track the preceding file position.
	if ws, ok := w.(io.Seeker); ok {
		pos, err := ws.Seek(0, io.SeekCurrent)
		if err == nil {
			off = pos
		}
	}
	if _, err := w.Write(packet); err != nil {
		return off, err
	}
	return off, nil
}

// ReadFile reads all length-framed event records.
func ReadFile(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.New(fault.IO, fault.StageEvent, path, -1, -1, "open event file: %v", err)
	}
	defer f.Close()

	var unwrap Unwrapper
	var out []Event
	for {
		lenPos, _ := f.Seek(0, io.SeekCurrent)
		var lenBuf [4]byte
		n, err := io.ReadFull(f, lenBuf[:])
		if err == io.EOF {
			return out, nil
		}
		if err == io.ErrUnexpectedEOF {
			off := lenPos + int64(n)
			return nil, fault.New(fault.EventParse, fault.StageEvent, path, off, -1, "truncated record length")
		}
		if err != nil {
			return nil, fault.Wrap(err, fault.EventParse, fault.StageEvent, path, lenPos, -1)
		}
		size := binary.BigEndian.Uint32(lenBuf[:])
		if size == 0 || size > maxPacket {
			return nil, fault.New(fault.EventParse, fault.StageEvent, path, lenPos+4, -1, "invalid packet length %d", size)
		}
		pktOff, _ := f.Seek(0, io.SeekCurrent)
		buf := make([]byte, size)
		n, err = io.ReadFull(f, buf)
		if err != nil {
			off := pktOff + int64(n)
			return nil, fault.New(fault.EventParse, fault.StageEvent, path, off, -1, "truncated RTP packet: %v", err)
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf); err != nil {
			return nil, fault.New(fault.EventParse, fault.StageEvent, path, pktOff, -1, "RTP unmarshal: %v", err)
		}
		kind, id, perr := ParsePayload(pkt.Payload)
		if perr != nil {
			return nil, fault.New(fault.EventParse, fault.StageEvent, path, pktOff, int64(pkt.Timestamp), "payload: %v", perr)
		}
		out = append(out, Event{
			ID: id, Kind: kind, RawTimestamp: pkt.Timestamp,
			Tick: unwrap.Unwrap(pkt.Timestamp), Sequence: pkt.SequenceNumber, RawOffset: pktOff,
		})
	}
}

// WrappedTimestampAt returns the raw 32-bit timestamp d event-clock ticks after base.
func WrappedTimestampAt(base uint32, d int64) uint32 {
	return uint32(int64(base) + d)
}

// NearWrapBase is 500 ms before the 32-bit event clock wraps.
func NearWrapBase() uint32 { return math.MaxUint32 - 45_000 }
