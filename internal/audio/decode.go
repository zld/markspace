package audio

import (
	"fmt"
)

// DecodeResult is PCM decoded from a container sample record plus the facts
// carried by that record. The kernel only decodes its own verifiable PCM
// framing; arbitrary AAC is intentionally out of scope.
type DecodeResult struct {
	Buffer *Buffer
	Rate   int
}

// PeekChannels reads the channel count from a record header without decoding
// the payload. It is used to establish the layout before allocation.
func PeekChannels(record []byte) (channels, rate int, frames int, err error) {
	if len(record) < 24 {
		return 0, 0, 0, fmt.Errorf("audio: record shorter than header")
	}
	if record[0] != 0xAF || record[1] != 0x01 {
		return 0, 0, 0, fmt.Errorf("audio: record magic mismatch")
	}
	channels = int(record[3])
	rate = int(be32(record[4:]))
	payload := int(be32(record[8:]))
	frames = int(be32(record[16:]))
	if channels <= 0 || channels > 32 || rate <= 0 {
		return 0, 0, 0, fmt.Errorf("audio: implausible record header")
	}
	if payload != frames*channels*2 || 24+payload > len(record) {
		return 0, 0, 0, fmt.Errorf("audio: record payload length mismatch")
	}
	return channels, rate, frames, nil
}

// DecodeRecord converts one verified record payload into a float buffer.
// Samples are int16 little-endian, matching the fixture writer. Values are
// divided by 32768 so the nominal range is [-1,1).
func DecodeRecord(record []byte, layout Layout) (*Buffer, error) {
	channels, _, frames, err := PeekChannels(record)
	if err != nil {
		return nil, err
	}
	if channels != layout.NumChannels() {
		return nil, fmt.Errorf("audio: record has %d channels, buffer layout %s needs %d",
			channels, layout.Name, layout.NumChannels())
	}
	buf, err := NewBuffer(layout, frames)
	if err != nil {
		return nil, err
	}
	off := 24
	for i := 0; i < frames; i++ {
		for c := 0; c < channels; c++ {
			v := int16(uint16(record[off]) | uint16(record[off+1])<<8)
			buf.Data[i*channels+c] = float64(v) / 32768.0
			off += 2
		}
	}
	return buf, nil
}

// AppendBuffers joins contiguous buffers of the same layout into one.
func AppendBuffers(bufs ...*Buffer) (*Buffer, error) {
	if len(bufs) == 0 {
		return nil, fmt.Errorf("audio: nothing to append")
	}
	layout := bufs[0].Layout
	total := 0
	for _, b := range bufs {
		if b == nil || b.Layout.Name != layout.Name {
			return nil, fmt.Errorf("audio: cannot append mismatched layouts")
		}
		total += b.Frames
	}
	out, err := NewBuffer(layout, total)
	if err != nil {
		return nil, err
	}
	pos := 0
	for _, b := range bufs {
		copy(out.Data[pos:], b.Data)
		pos += len(b.Data)
	}
	out.Start = bufs[0].Start
	return out, nil
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
