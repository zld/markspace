package metadata

import (
	"encoding/binary"
	"math"

	"github.com/zld/avpipe/internal/clock"
	"github.com/zld/avpipe/internal/fault"
)

// safeCursor is the only bounds-checked reader used by binary parsers.
type safeCursor struct {
	data      []byte
	off       int
	component string
}

func (c *safeCursor) remaining() int { return len(c.data) - c.off }

func (c *safeCursor) need(n int, what string) ([]byte, error) {
	if n < 0 || c.off < 0 || n > c.remaining() {
		return nil, fault.Truncated(c.component, int64(c.off), int64(len(c.data)), what)
	}
	start := c.off
	c.off += n
	return c.data[start:c.off:c.off], nil
}

func (c *safeCursor) u8(what string) (byte, error) {
	b, err := c.need(1, what)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (c *safeCursor) u16(what string) (uint16, error) {
	b, err := c.need(2, what)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func (c *safeCursor) u32(what string) (uint32, error) {
	b, err := c.need(4, what)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (c *safeCursor) u64(what string) (uint64, error) {
	b, err := c.need(8, what)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}

func appendU16(buf []byte, v uint16) []byte {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	return append(buf, b[:]...)
}

func appendU32(buf []byte, v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return append(buf, b[:]...)
}

func appendU64(buf []byte, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return append(buf, b[:]...)
}

// BuildTimedRecords encodes events in the deterministic AVM1 fixture format.
// It is used because the acceptance pipeline only processes self-generated
// samples.
func BuildTimedRecords(events []Event) []byte {
	buf := []byte("AVM1")
	if len(events) > 0 && events[0].Start.Timescale != 0 {
		buf = appendU32(buf, events[0].Start.Timescale)
	} else {
		buf = appendU32(buf, 1000)
	}
	for _, e := range events {
		track := []byte(e.Track)
		kind := []byte(e.Kind)
		text := []byte(e.Text)
		buf = append(buf, byte(len(track)))
		buf = append(buf, track...)
		buf = append(buf, byte(len(kind)))
		buf = append(buf, kind...)
		buf = appendU64(buf, uint64(e.Start.Ticks))
		var duration uint32
		if e.End != nil {
			end := e.End.In(e.Start.Timescale)
			if d := end.Ticks - e.Start.Ticks; d >= 0 && d <= math.MaxUint32 {
				duration = uint32(d)
			}
		}
		buf = appendU32(buf, duration)
		buf = appendU32(buf, uint32(len(text)))
		buf = append(buf, text...)
	}
	return buf
}

// ParseTimedRecords parses AVM1 fixtures and reports the first absent offset.
func ParseTimedRecords(data []byte) ([]Event, error) {
	c := &safeCursor{data: data, component: "metadata/avm1"}
	magic, err := c.need(4, "magic")
	if err != nil {
		return nil, err
	}
	if string(magic) != "AVM1" {
		return nil, fault.New(fault.KindInvalid, "bad AVM1 magic %q", magic).At("metadata/avm1", 0, 0)
	}
	scale, err := c.u32("timescale")
	if err != nil {
		return nil, err
	}
	if scale == 0 {
		return nil, fault.New(fault.KindInvalid, "zero timescale").At("metadata/avm1", 4, 0)
	}

	var events []Event
	for c.off < len(data) {
		eventOffset := c.off
		trackLen, err := c.u8("track length")
		if err != nil {
			return events, err
		}
		trackRaw, err := c.need(int(trackLen), "track")
		if err != nil {
			return events, err
		}
		kindLen, err := c.u8("kind length")
		if err != nil {
			return events, err
		}
		kindRaw, err := c.need(int(kindLen), "kind")
		if err != nil {
			return events, err
		}
		startRaw, err := c.u64("start ticks")
		if err != nil {
			return events, err
		}
		duration, err := c.u32("duration")
		if err != nil {
			return events, err
		}
		textLen, err := c.u32("text length")
		if err != nil {
			return events, err
		}
		textRaw, err := c.need(int(textLen), "text")
		if err != nil {
			return events, err
		}
		start := clock.NewTimestamp(clock.Ticks(startRaw), scale)
		var end *clock.Timestamp
		if duration > 0 {
			e := clock.NewTimestamp(start.Ticks+clock.Ticks(duration), scale)
			end = &e
		}
		text := string(textRaw)
		events = append(events, Event{
			Track:        string(trackRaw),
			Kind:         string(kindRaw),
			Start:        start,
			End:          end,
			Text:         text,
			Digest:       DigestContent(textRaw),
			SourceOffset: int64(eventOffset),
		})
	}
	return events, nil
}

// BuildID3Priv creates an ID3v2.3 tag containing one PRIV timed event.
func BuildID3Priv(e Event) []byte {
	payload := []byte("com.avpipe.ticks")
	payload = append(payload, 0)
	payload = appendU32(payload, e.Start.Timescale)
	payload = appendU64(payload, uint64(e.Start.Ticks))
	var duration uint32
	if e.End != nil {
		end := e.End.In(e.Start.Timescale)
		if d := end.Ticks - e.Start.Ticks; d >= 0 && d <= math.MaxUint32 {
			duration = uint32(d)
		}
	}
	payload = appendU32(payload, duration)
	payload = append(payload, []byte(e.Text)...)

	frame := []byte("PRIV")
	frame = appendU32(frame, uint32(len(payload)))
	frame = appendU16(frame, 0)
	frame = append(frame, payload...)

	tag := []byte("ID3")
	tag = append(tag, 3, 0, 0) // ID3v2.3, no flags
	tag = append(tag, synchsafe(uint32(len(frame)))...)
	tag = append(tag, frame...)
	return tag
}

// ParseID3Priv parses self-generated ID3 PRIV timed metadata.
func ParseID3Priv(data []byte) ([]Event, error) {
	const component = "metadata/id3"
	if len(data) < 10 {
		return nil, fault.Truncated(component, 0, int64(len(data)), "ID3 header")
	}
	if string(data[0:3]) != "ID3" {
		return nil, fault.New(fault.KindInvalid, "missing ID3 magic").At(component, 0, 0)
	}
	tagSize := fromSynchsafe(data[6:10])
	tagEnd := 10 + int(tagSize)
	if tagEnd > len(data) {
		return nil, fault.Truncated(component, 6, int64(len(data)), "ID3 tag payload")
	}

	var events []Event
	off := 10
	for off+10 <= tagEnd {
		id := string(data[off : off+4])
		size := int(binary.BigEndian.Uint32(data[off+4 : off+8]))
		frameEnd := off + 10 + size
		if frameEnd > tagEnd || frameEnd > len(data) {
			return events, fault.Truncated(component, int64(off+4), int64(len(data)), "ID3 frame "+id)
		}
		if id == "PRIV" {
			e, err := parsePrivPayload(data[off+10 : frameEnd])
			if err != nil {
				return events, err
			}
			e.SourceOffset = int64(off)
			events = append(events, e)
		}
		off = frameEnd
	}
	return events, nil
}

func parsePrivPayload(payload []byte) (Event, error) {
	const component = "metadata/id3-priv"
	idx := indexNull(payload)
	if idx < 0 {
		return Event{}, fault.New(fault.KindInvalid, "missing PRIV owner terminator").At(component, 0, 0)
	}
	p := payload[idx+1:]
	if len(p) < 16 {
		return Event{}, fault.Truncated(component, int64(idx+1), int64(len(payload)), "PRIV timing fields")
	}
	scale := binary.BigEndian.Uint32(p[0:4])
	start := clock.Ticks(binary.BigEndian.Uint64(p[4:12]))
	duration := binary.BigEndian.Uint32(p[12:16])
	text := p[16:]
	if scale == 0 {
		return Event{}, fault.New(fault.KindInvalid, "zero PRIV timescale").At(component, 0, 0)
	}
	ts := clock.NewTimestamp(start, scale)
	var end *clock.Timestamp
	if duration > 0 {
		e := clock.NewTimestamp(start+clock.Ticks(duration), scale)
		end = &e
	}
	return Event{
		Track:  "id3",
		Kind:   "id3-priv",
		Start:  ts,
		End:    end,
		Text:   string(text),
		Digest: DigestContent(text),
	}, nil
}

func indexNull(b []byte) int {
	for i, v := range b {
		if v == 0 {
			return i
		}
	}
	return -1
}

func synchsafe(v uint32) []byte {
	return []byte{byte((v >> 21) & 0x7f), byte((v >> 14) & 0x7f), byte((v >> 7) & 0x7f), byte(v & 0x7f)}
}

func fromSynchsafe(b []byte) uint32 {
	return uint32(b[0]&0x7f)<<21 | uint32(b[1]&0x7f)<<14 | uint32(b[2]&0x7f)<<7 | uint32(b[3]&0x7f)
}
