package rtp

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/pion/rtp"
)

// TraceMagic identifies pwflow's length-framed captured RTP record format.
const TraceMagic = "PWRT0001"

const maxFramePacket = 64 << 10

// WriteTrace writes magic followed by [arrival_ns uint64][length uint16][RTP].
func WriteTrace(w io.Writer, records []Captured) error {
	if _, err := io.WriteString(w, TraceMagic); err != nil {
		return err
	}
	var hdr [10]byte
	for _, rec := range records {
		raw, err := rec.Packet.Marshal()
		if err != nil {
			return err
		}
		if len(raw) > maxFramePacket {
			return fmt.Errorf("rtp packet too large: %d", len(raw))
		}
		binary.BigEndian.PutUint64(hdr[:8], uint64(rec.ArrivalNS))
		binary.BigEndian.PutUint16(hdr[8:], uint16(len(raw)))
		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := w.Write(raw); err != nil {
			return err
		}
	}
	return nil
}

// ReadTrace reads the pwflow framed format. It uses pion/rtp to unmarshal
// every captured packet and rejects malformed records explicitly.
func ReadTrace(r io.Reader) ([]Captured, error) {
	magic := make([]byte, len(TraceMagic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, fmt.Errorf("read rtp trace magic: %w", err)
	}
	if string(magic) != TraceMagic {
		return nil, fmt.Errorf("bad rtp trace magic %q", string(magic))
	}
	var out []Captured
	var hdr [10]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err == io.EOF {
			return out, nil
		} else if err == io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("truncated rtp record header")
		} else if err != nil {
			return nil, err
		}
		arrival := int64(binary.BigEndian.Uint64(hdr[:8]))
		n := int(binary.BigEndian.Uint16(hdr[8:]))
		if n == 0 || n > maxFramePacket {
			return nil, fmt.Errorf("invalid rtp frame length %d", n)
		}
		raw := make([]byte, n)
		if _, err := io.ReadFull(r, raw); err != nil {
			return nil, fmt.Errorf("read rtp packet: %w", err)
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			return nil, fmt.Errorf("pion/rtp parse at record %d: %w", len(out), err)
		}
		out = append(out, Captured{ArrivalNS: arrival, Packet: pkt})
	}
}
