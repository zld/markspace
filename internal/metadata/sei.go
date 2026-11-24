package metadata

import (
	"encoding/binary"

	"github.com/zld/avpipe/internal/clock"
	"github.com/zld/avpipe/internal/fault"
)

// avpipeSEIUUID is a deterministic fixture UUID used to identify avpipe user
// data SEI messages. It is intentionally not a privacy-sensitive identifier.
var avpipeSEIUUID = [16]byte{
	0xa7, 0x7e, 0x11, 0xce, 0x9f, 0x33, 0x4b, 0x8a,
	0x9c, 0x17, 0x6a, 0x50, 0x41, 0x56, 0x50, 0x45,
}

// BuildSEI creates a minimal Annex-B user-data-unregistered SEI NAL unit.
func BuildSEI(e Event) []byte {
	var timing [16]byte
	binary.BigEndian.PutUint32(timing[0:4], e.Start.Timescale)
	binary.BigEndian.PutUint64(timing[4:12], uint64(e.Start.Ticks))
	var duration uint32
	if e.End != nil {
		end := e.End.In(e.Start.Timescale)
		if d := end.Ticks - e.Start.Ticks; d >= 0 {
			duration = uint32(d)
		}
	}
	binary.BigEndian.PutUint32(timing[12:16], duration)

	payload := append([]byte{}, avpipeSEIUUID[:]...)
	payload = append(payload, timing[:]...)
	payload = append(payload, []byte(e.Text)...)

	out := []byte{0, 0, 0, 1, 0x06, 0x05, byte(len(payload))}
	out = append(out, payload...)
	out = append(out, 0x80) // RBSP stop bit
	return out
}

// ParseSEI parses the self-generated SEI fixture. Unlike a full H.264 parser
// it only understands the avpipe UUID payload and performs all reads safely.
func ParseSEI(data []byte) ([]Event, error) {
	const component = "metadata/sei"
	nal := findSEINAL(data)
	if nal < 0 {
		return nil, fault.New(fault.KindInvalid, "no avpipe SEI NAL start code").At(component, 0, 0)
	}
	c := &safeCursor{data: data, off: nal, component: component}
	header, err := c.u8("NAL header")
	if err != nil {
		return nil, err
	}
	if header&0x1f != 6 {
		return nil, fault.New(fault.KindInvalid, "NAL type %d is not SEI", header&0x1f).At(component, int64(nal), 0)
	}

	payloadType := 0
	for {
		b, err := c.u8("SEI payload type")
		if err != nil {
			return nil, err
		}
		payloadType += int(b)
		if b != 0xff {
			break
		}
	}
	if payloadType != 5 {
		return nil, fault.New(fault.KindInvalid, "unsupported SEI payload type %d", payloadType).At(component, int64(c.off), 0)
	}

	size := 0
	for {
		b, err := c.u8("SEI payload size")
		if err != nil {
			return nil, err
		}
		size += int(b)
		if b != 0xff {
			break
		}
	}
	payload, err := c.need(size, "SEI payload")
	if err != nil {
		return nil, err
	}
	if len(payload) < 32 {
		return nil, fault.Truncated(component, int64(c.off), int64(len(data)), "SEI UUID and timing")
	}
	for i := 0; i < 16; i++ {
		if payload[i] != avpipeSEIUUID[i] {
			return nil, fault.New(fault.KindInvalid, "unknown SEI UUID").At(component, int64(c.off), 0)
		}
	}
	scale := binary.BigEndian.Uint32(payload[16:20])
	start := clock.Ticks(binary.BigEndian.Uint64(payload[20:28]))
	duration := binary.BigEndian.Uint32(payload[28:32])
	text := payload[32:]
	if scale == 0 {
		return nil, fault.New(fault.KindInvalid, "zero SEI timescale").At(component, int64(c.off+16), 0)
	}
	ts := clock.NewTimestamp(start, scale)
	var end *clock.Timestamp
	if duration > 0 {
		e := clock.NewTimestamp(start+clock.Ticks(duration), scale)
		end = &e
	}
	return []Event{{
		Track:        "sei",
		Kind:         "sei-user-data",
		Start:        ts,
		End:          end,
		Text:         string(text),
		Digest:       DigestContent(text),
		SourceOffset: int64(nal),
	}}, nil
}

func findSEINAL(data []byte) int {
	for i := 0; i+5 < len(data); i++ {
		if i+4 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			if data[i+4] == 0x06 {
				return i + 4
			}
		}
		if i+3 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 && data[i+3] == 0x06 {
			return i + 3
		}
	}
	return -1
}
