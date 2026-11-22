package ingest

import (
	"encoding/binary"
	"fmt"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

// SEIMessage is one decoded SEI payload.
type SEIMessage struct {
	Type    int
	Payload []byte
}

// SEITimestamp is the user-data-unregistered contents the generator embeds:
// a 64-bit presentation timestamp in microseconds plus a label.
type SEITimestamp struct {
	UUID     [16]byte
	TimeUS   int64
	Kind     string
	RawBytes []byte
}

// SEI UUID used by the sample fixture generator (a fixed private UUID).
var seiUUID = [16]byte{0x6d, 0x65, 0x64, 0x69, 0x61, 0x2d, 0x74, 0x73, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}

// parseAnnexB splits an Annex B byte stream into NAL units and strips the
// emulation-prevention bytes from each unit.
func parseAnnexB(data []byte) [][]byte {
	var nals [][]byte
	i := 0
	start := -1
	for i+2 < len(data) {
		if data[i] == 0 && data[i+1] == 0 && (data[i+2] == 1 || (i+3 < len(data) && data[i+2] == 0 && data[i+3] == 1)) {
			if start >= 0 {
				nals = append(nals, unescapeRBSP(data[start:i]))
			}
			if data[i+2] == 1 {
				i += 3
			} else {
				i += 4
			}
			start = i
			continue
		}
		i++
	}
	if start >= 0 && start < len(data) {
		nals = append(nals, unescapeRBSP(data[start:]))
	}
	return nals
}

func unescapeRBSP(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if i+2 < len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 3 {
			out = append(out, 0, 0)
			i += 2
			continue
		}
		out = append(out, b[i])
	}
	return out
}

// parseSEINAL decodes the SEI messages in a NAL unit (type 6), with or
// without the 1-byte NAL header.
func parseSEINAL(nal []byte) ([]SEIMessage, error) {
	if len(nal) == 0 {
		return nil, fmt.Errorf("empty NAL")
	}
	body := nal
	if nal[0]&0x1f == 6 {
		body = nal[1:]
	}
	var msgs []SEIMessage
	for len(body) > 0 {
		if body[0] == 0x80 {
			break // rbsp_trailing_bits
		}
		typ, n := readSEINum(body)
		if n == 0 {
			return msgs, fmt.Errorf("malformed SEI payload type")
		}
		body = body[n:]
		size, n := readSEINum(body)
		if n == 0 {
			return msgs, fmt.Errorf("malformed SEI payload size")
		}
		body = body[n:]
		if size > len(body) {
			return msgs, fmt.Errorf("SEI payload claims %d bytes, %d remain", size, len(body))
		}
		msgs = append(msgs, SEIMessage{Type: typ, Payload: body[:size]})
		body = body[size:]
	}
	return msgs, nil
}

func readSEINum(b []byte) (int, int) {
	total, n := 0, 0
	for n < len(b) && b[n] == 0xff {
		total += 255
		n++
	}
	if n >= len(b) {
		return 0, 0
	}
	total += int(b[n])
	return total, n + 1
}

// ParseSEI extracts every SEI message from an access unit. Both Annex B
// (start-code) and length-prefixed (MP4/AVCC, four-byte big-endian lengths)
// layouts are recognized, so a sample taken straight from an MP4 table can be
// inspected with the same call as an RTP payload.
func ParseSEI(accessUnit []byte) ([]SEIMessage, error) {
	var out []SEIMessage
	var firstErr error
	nals := parseAnnexB(accessUnit)
	if len(nals) == 0 {
		nals = parseLengthPrefixed(accessUnit)
	}
	if len(nals) == 0 {
		nals = [][]byte{accessUnit}
	}
	for _, nal := range nals {
		if len(nal) == 0 || nal[0]&0x1f != 6 {
			continue
		}
		msgs, err := parseSEINAL(nal)
		out = append(out, msgs...)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return out, firstErr
}

// parseLengthPrefixed splits an AVCC-style access unit (4-byte big-endian NAL
// lengths). It returns nil when the buffer does not decode cleanly.
func parseLengthPrefixed(data []byte) [][]byte {
	var nals [][]byte
	for i := 0; i+4 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[i : i+4]))
		if n <= 0 || i+4+n > len(data) {
			return nil
		}
		// AVCC NAL units carry emulation-prevention bytes just like Annex B.
		nals = append(nals, unescapeRBSP(data[i+4:i+4+n]))
		i += 4 + n
	}
	return nals
}

// SEITimestamps keeps only user-data-unregistered messages carrying the
// fixture UUID and decodes their timestamp.
func SEITimestamps(msgs []SEIMessage) ([]SEITimestamp, error) {
	var out []SEITimestamp
	var firstErr error
	for _, m := range msgs {
		if m.Type != 5 || len(m.Payload) < 16 {
			continue
		}
		var u [16]byte
		copy(u[:], m.Payload[:16])
		if u != seiUUID {
			continue
		}
		body := m.Payload[16:]
		ts := SEITimestamp{UUID: u, RawBytes: append([]byte(nil), body...)}
		if len(body) >= 8 {
			ts.TimeUS = int64(binary.BigEndian.Uint64(body[:8]))
		}
		if len(body) > 8 {
			ts.Kind = string(body[8:])
		}
		if len(body) < 8 {
			if firstErr == nil {
				firstErr = fmt.Errorf("SEI timestamp payload too short: %d bytes", len(body))
			}
			continue
		}
		out = append(out, ts)
	}
	return out, firstErr
}

// BuildSEINAL constructs one Annex B NAL unit carrying the fixture SEI. It is
// used by the generator and by tests to build inputs without external files.
func BuildSEINAL(timeUS int64, kind string) []byte {
	payload := make([]byte, 16+8+len(kind))
	copy(payload, seiUUID[:])
	binary.BigEndian.PutUint64(payload[16:24], uint64(timeUS))
	copy(payload[24:], kind)
	var body []byte
	body = appendSEINum(body, 5)
	body = appendSEINum(body, len(payload))
	body = append(body, payload...)
	body = append(body, 0x80)
	nal := append([]byte{0x06}, rbspEscape(body)...)
	// One access-delimiter NAL, the SEI NAL, and the start code of the next
	// unit, so the parser must detect the SEI by NAL type, not by position.
	au := []byte{0x00, 0x00, 0x00, 0x01, 0x09, 0xf0}
	au = append(au, 0x00, 0x00, 0x00, 0x01)
	au = append(au, nal...)
	return au
}

func appendSEINum(b []byte, v int) []byte {
	for v >= 255 {
		b = append(b, 0xff)
		v -= 255
	}
	return append(b, byte(v))
}

// rbspEscape inserts emulation-prevention bytes so the NAL can sit in an
// Annex B stream.
func rbspEscape(b []byte) []byte {
	out := make([]byte, 0, len(b)+4)
	zeros := 0
	for _, v := range b {
		if zeros >= 2 && v <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, v)
		if v == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// SEIColors extracts color declarations carried in user-data-unregistered SEI
// messages whose payload starts with the four-byte "nclx" marker. This is the
// bitstream's claim, independent of the container's colr box.
func SEIColors(msgs []SEIMessage) []model.ColorDeclaration {
	var out []model.ColorDeclaration
	for _, m := range msgs {
		if m.Type != 5 {
			continue
		}
		// Accept both a bare "nclx" payload and the T.35-wrapped form
		// (country code 0xB5, provider 0x0031, then "nclx").
		body := m.Payload
		if len(body) >= 3 && body[0] == 0xB5 && body[1] == 0x00 && body[2] == 0x31 {
			body = body[3:]
		}
		if len(body) < 10 || string(body[:4]) != "nclx" {
			continue
		}
		out = append(out, model.ColorDeclaration{
			Source:    model.ColorFromBitstream,
			Primaries: binary.BigEndian.Uint16(body[4:6]),
			Transfer:  binary.BigEndian.Uint16(body[6:8]),
			Matrix:    binary.BigEndian.Uint16(body[8:10]),
			FullRange: len(body) > 10 && body[10]&0x80 != 0,
		})
	}
	return out
}

// SEIEvents converts SEI timestamps into model events using the track
// repairer. Times are microseconds; the events are decoded at 1 MHz.
func SEIEvents(track model.TrackID, times []SEITimestamp) ([]model.Event, error) {
	r, err := newMetadataClock(track, 1000000)
	if err != nil {
		return nil, err
	}
	out := make([]model.Event, 0, len(times))
	for i, t := range times {
		ts, err := r.Decode(t.TimeUS)
		if err != nil {
			return out, err
		}
		out = append(out, model.Event{
			Kind:  model.MetaSEI,
			Track: track,
			ID:    fmt.Sprintf("sei-%d", i),
			Time:  ts,
			Text:  t.Kind,
		})
	}
	return out, nil
}

// newMetadataClock builds a repairer for a metadata stream whose raw unit is
// microseconds and whose clock never wraps.
func newMetadataClock(track model.TrackID, timescale uint32) (*clock.Repairer, error) {
	return clock.NewRepairer(track, clock.RepairOptions{
		Timescale:   timescale,
		Modulus:     0,
		JitterTicks: 1,
	})
}
