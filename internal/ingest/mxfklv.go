package ingest

import (
	"encoding/binary"
	"fmt"

	"mediapipe/internal/fault"
	"mediapipe/internal/limits"
	"mediapipe/internal/mxfkv"
)

// klvItem is one located KLV triplet in the restricted MXF input.
type klvItem struct {
	key     [16]byte
	offset  int64 // absolute offset of the key
	value   []byte
	unknown bool
}

// readBERLength decodes the BER-encoded length at data[pos] and returns the
// length plus the number of header bytes consumed.
func readBERLength(data []byte, pos int) (length int64, hdrLen int, ok bool) {
	if pos >= len(data) {
		return 0, 0, false
	}
	first := data[pos]
	if first < 0x80 {
		return int64(first), 1, true
	}
	n := int(first & 0x7f)
	if n == 0 || n > 8 {
		return 0, 0, false
	}
	if pos+1+n > len(data) {
		return 0, 0, false
	}
	var v uint64
	for i := 0; i < n; i++ {
		v = v<<8 | uint64(data[pos+1+i])
	}
	return int64(v), 1 + n, true
}

// scanKLV walks all KLV triplets. A declared length past EOF becomes a
// recoverable truncation fault at the exact key offset.
func scanKLV(file string, data []byte, errs *fault.Batch) []klvItem {
	var items []klvItem
	pos := 0
	for pos < len(data) {
		if len(data)-pos < mxfkv.KeyLen+1 {
			errs.Add(fault.New(fault.KindTruncated,
				"KLV header truncated", file, int64(pos), true))
			return items
		}
		var key [16]byte
		copy(key[:], data[pos:pos+mxfkv.KeyLen])
		keyOff := int64(pos)
		length, hdrLen, ok := readBERLength(data, pos+mxfkv.KeyLen)
		if !ok {
			errs.Add(fault.New(fault.KindTruncated,
				"invalid BER length", file, keyOff, true))
			return items
		}
		start := pos + mxfkv.KeyLen + hdrLen
		if length > limits.MaxBoxBytes {
			errs.Add(limits.Exceeded(file, "KLV value length", keyOff,
				length, limits.MaxBoxBytes))
			return items
		}
		if int64(start)+length > int64(len(data)) {
			errs.Add(fault.New(fault.KindTruncated,
				fmt.Sprintf("KLV declares %d bytes but %d remain",
					length, len(data)-start),
				file, keyOff, true))
			return items
		}
		if !mxfkv.IsPrefix(key) {
			errs.Add(fault.New(fault.KindInvalidInput,
				"key missing SMPTE UL prefix", file, keyOff, false))
			return items
		}
		items = append(items, klvItem{
			key:     key,
			offset:  keyOff,
			value:   data[start : start+int(length)],
			unknown: !mxfkv.IsKnown(key),
		})
		pos = start + int(length)
	}
	return items
}

// localItem is one tag/length/value triplet inside a local set.
type localItem struct {
	tag    uint16
	value  []byte
	offset int64
}

// parseLocalSet decodes a local-set value into bounded local items.
func parseLocalSet(file string, base int64, data []byte, errs *fault.Batch) []localItem {
	var out []localItem
	pos := 0
	for pos < len(data) {
		if pos+2 > len(data) {
			errs.Add(fault.New(fault.KindTruncated,
				"local item tag truncated", file, base+int64(pos), true))
			return out
		}
		tag := binary.BigEndian.Uint16(data[pos:])
		length, hdrLen, ok := readBERLength(data, pos+2)
		if !ok {
			errs.Add(fault.New(fault.KindTruncated,
				"local item BER length invalid", file, base+int64(pos), true))
			return out
		}
		start := pos + 2 + hdrLen
		if start+int(length) > len(data) {
			errs.Add(fault.New(fault.KindTruncated,
				"local item overruns set", file, base+int64(pos), true))
			return out
		}
		out = append(out, localItem{
			tag:    tag,
			value:  data[start : start+int(length)],
			offset: base + int64(pos),
		})
		pos = start + int(length)
	}
	return out
}
