package index

import (
	"fmt"

	"mp4seek/internal/bmff"
)

// SampleEntry describes the first stsd entry of a track.
type SampleEntry struct {
	Format    string // e.g. "avc1", "mp4a", "encv"
	AvcC      []byte // avcC payload for AVC entries, nil otherwise
	Encrypted bool   // encv/enca or other encrypted scheme
}

// IsAVC reports whether the entry is an AVC video entry.
func (e SampleEntry) IsAVC() bool { return e.Format == "avc1" || e.Format == "avc3" }

// childBoxes iterates box records inside a byte slice (sample entry tail).
func childBoxes(data []byte, path string, fn func(typ string, payload []byte) error) error {
	off := 0
	for off < len(data) {
		if len(data)-off < 8 {
			return fmt.Errorf("%w: %s: trailing %d bytes in entry", bmff.ErrCorrupt, path, len(data)-off)
		}
		size := int(uint32(data[off])<<24 | uint32(data[off+1])<<16 | uint32(data[off+2])<<8 | uint32(data[off+3]))
		typ := string(data[off+4 : off+8])
		if size < 8 || size > len(data)-off {
			return fmt.Errorf("%w: %s: child box %q size %d out of range", bmff.ErrCorrupt, path, typ, size)
		}
		if err := fn(typ, data[off+8:off+size]); err != nil {
			return err
		}
		off += size
	}
	return nil
}

// ParseStsd decodes the first entry of an stsd box payload.
func ParseStsd(data []byte, path string) (SampleEntry, error) {
	c := &cursor{data: data, path: path + "/stsd"}
	if _, _, err := c.fullBoxHeader(); err != nil {
		return SampleEntry{}, err
	}
	count, err := c.u32()
	if err != nil {
		return SampleEntry{}, err
	}
	if count < 1 {
		return SampleEntry{}, c.fail("stsd has no entries")
	}
	size, err := c.u32()
	if err != nil {
		return SampleEntry{}, err
	}
	format, err := c.take(4)
	if err != nil {
		return SampleEntry{}, err
	}
	entry := SampleEntry{Format: string(format)}
	if size < 8 || int(size)-8 > c.remaining() {
		return SampleEntry{}, c.fail(fmt.Sprintf("stsd entry size %d out of range", size))
	}
	body, err := c.take(int(size) - 8)
	if err != nil {
		return SampleEntry{}, err
	}
	switch entry.Format {
	case "encv", "enca":
		entry.Encrypted = true
		return entry, nil
	}
	// Visual and audio sample entries have fixed headers before child
	// boxes: 78 bytes (visual, after size+format) or 28 (audio).
	head := 78
	if entry.Format == "mp4a" || entry.Format == "ac-3" || entry.Format == "ec-3" {
		head = 28
	}
	if len(body) < head {
		return SampleEntry{}, c.fail(fmt.Sprintf("stsd entry %q too short: %d bytes", entry.Format, len(body)))
	}
	err = childBoxes(body[head:], path, func(typ string, payload []byte) error {
		if typ == "avcC" {
			entry.AvcC = append([]byte(nil), payload...)
		}
		return nil
	})
	if err != nil {
		return SampleEntry{}, err
	}
	return entry, nil
}
