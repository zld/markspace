package testdata

import (
	"encoding/binary"
	"os"
)

// Corruption identifies the five self-generated anomaly classes.
type Corruption string

const (
	CorruptTruncatedBox Corruption = "truncated-box"
	CorruptTruncatedKLV Corruption = "truncated-klv"
	CorruptConflictMeta Corruption = "conflict-metadata"
	CorruptRTPWrap      Corruption = "rtp-wrap"
	CorruptDrift        Corruption = "clock-drift"
	CorruptVFRDupPTS    Corruption = "vfr-dup-pts"
)

// AllCorruptions is the anomaly catalog. The five required classes map to
// six concrete variants because the truncation class has an MP4 box form and
// an MXF KLV form.
func AllCorruptions() []Corruption {
	return []Corruption{
		CorruptTruncatedBox, CorruptTruncatedKLV, CorruptConflictMeta,
		CorruptRTPWrap, CorruptDrift, CorruptVFRDupPTS,
	}
}

// TruncateBox writes src to dst with the final box size enlarged past EOF,
// simulating a truncated MP4 container. The original bytes are preserved;
// only the last box header is patched, so the declared byte offset remains
// traceable.
func TruncateBox(src, dst string) (offset int64, err error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return 0, err
	}
	last := lastBoxStart(data)
	// Declare a payload 1 KiB larger than the file actually contains.
	binary.BigEndian.PutUint32(data[last:], uint32(len(data)-int(last))+1024)
	return int64(last), os.WriteFile(dst, data, 0o644)
}

// TruncateKLV writes src to dst and cuts the final KLV short of its declared
// BER length, returning the offset at which truncation begins.
func TruncateKLV(src, dst string) (offset int64, err error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return 0, err
	}
	// Find the start of the final KLV by scanning for a 16-byte UL prefix.
	start := -1
	for i := len(data) - 17; i >= 0; i-- {
		if data[i] == 0x06 && data[i+1] == 0x0e && data[i+2] == 0x2b && data[i+3] == 0x34 {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, os.ErrInvalid
	}
	// Keep the header and at least one payload byte, drop the rest.
	cut := start + 16 + 1 + len(data[start+17:])/2
	if cut >= len(data) {
		cut = len(data) - 1
	}
	return int64(start), os.WriteFile(dst, data[:cut], 0o644)
}

// lastBoxStart locates the start offset of the last top-level MP4 box.
func lastBoxStart(data []byte) int {
	pos := 0
	last := 0
	for pos+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[pos:]))
		if size < 8 || pos+size > len(data) {
			break
		}
		last = pos
		pos += size
	}
	return last
}

// PatchBoxSize overwrites the size field of the box beginning at offset.
func PatchBoxSize(path string, offset int64, size uint32) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	binary.BigEndian.PutUint32(data[offset:], size)
	return os.WriteFile(path, data, 0o644)
}

// FindBoxOffset scans top-level boxes and returns the offset of named type.
func FindBoxOffset(path, typ string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pos := 0
	for pos+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[pos:]))
		if size < 8 || pos+size > len(data) {
			return 0, os.ErrInvalid
		}
		if string(data[pos+4:pos+8]) == typ {
			return int64(pos), nil
		}
		pos += size
	}
	return 0, os.ErrNotExist
}
