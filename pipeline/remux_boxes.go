package pipeline

import (
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediacheck/timeline"
)

// boxWriter is the small subset of the go-mp4 writer used during remux.
type boxWriter struct{ w *mp4.Writer }

// start opens a box of the given four-character type.
func (b *boxWriter) start(typ string) (*mp4.BoxInfo, error) {
	return b.w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(typ)})
}

// end closes the most recently opened box and back-patches its size.
func (b *boxWriter) end() error {
	_, err := b.w.EndBox()
	return err
}

// raw writes opaque payload bytes inside the current box.
func (b *boxWriter) raw(p []byte) error {
	_, err := b.w.Write(p)
	return err
}

// u8/u16/u32 helpers serialise big-endian fixed-width fields.
func putU16(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }
func putU32(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}
func putU32b(b *[]byte, v uint32) { *b = append(*b, putU32(v)...) }
func putU16b(b *[]byte, v uint16) { *b = append(*b, putU16(v)...) }

// identityMatrix is the standard 0x00010000 unity transform, nine int32s.
func identityMatrix() []byte {
	m := make([]byte, 0, 36)
	for i, v := range [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		_ = i
		m = append(m, putU32(uint32(v))...)
	}
	return m
}

// fullBoxHeader emits version(1 byte)+flags(3 bytes).
func fullBox(version byte, flags uint32) []byte {
	return []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

// writeFtyp emits a minimal major-brand box.
func (b *boxWriter) writeFtyp() error {
	if _, err := b.start("ftyp"); err != nil {
		return err
	}
	payload := make([]byte, 0, 16)
	payload = append(payload, []byte("isom")...)
	putU32b(&payload, 0x00000200) // minor version 512
	payload = append(payload, []byte("isom")...)
	payload = append(payload, []byte("iso2")...)
	if err := b.raw(payload); err != nil {
		return err
	}
	return b.end()
}

// writeMdat writes the media payload box and returns its absolute offset.
func (b *boxWriter) writeMdat(video, audio [][]byte) (uint64, error) {
	if _, err := b.start("mdat"); err != nil {
		return 0, err
	}
	off, err := b.w.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	for _, p := range video {
		if err := b.raw(p); err != nil {
			return 0, err
		}
	}
	for _, p := range audio {
		if err := b.raw(p); err != nil {
			return 0, err
		}
	}
	if err := b.end(); err != nil {
		return 0, err
	}
	return uint64(off), nil
}

var _ = timeline.ClockRate
