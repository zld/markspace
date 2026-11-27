package ingest

import (
	"mediad/internal/model"
)

// Codec identifiers supported by the pipeline. Only raw Annex-B style H.264 /
// H.265 and low-overhead AV1 bitstreams plus a key-frame index are accepted.
const (
	CodecH264 = "h264"
	CodecH265 = "h265"
	CodecAV1  = "av1"
)

// IsSupportedCodec reports whether codec is one of the three enumerated
// raw-stream codecs.
func IsSupportedCodec(codec string) bool {
	switch codec {
	case CodecH264, CodecH265, CodecAV1:
		return true
	}
	return false
}

// IndexEntry points at one key frame (random-access point) inside the raw
// bitstream.
type IndexEntry struct {
	// Offset is the absolute byte offset of the key frame's first byte.
	Offset int64
	// Length is the byte length of the key frame's access unit (including any
	// start-code prefix for Annex-B streams).
	Length int64
	// Key is required to be true for every indexed entry; the index only
	// catalogs random-access points.
	Key bool
}

// Index is the validated set of key-frame pointers for one asset.
type Index struct {
	Codec   string
	Entries []IndexEntry
}

// ValidateIndex performs every index check required by the specification:
// codec support, per-entry offset/length overflow and positivity, monotonic
// strictly-increasing offsets, non-overlapping ordered units, and the key-frame
// marker on every entry. streamLen (<0 meaning unknown) bounds the final unit.
// The returned structured error carries the codec and the first reportable
// byte offset.
func ValidateIndex(codec string, entries []IndexEntry, streamLen int64) (*Index, error) {
	if !IsSupportedCodec(codec) {
		return nil, model.New(model.ErrUnknownCodec, codec, -1,
			"unsupported codec %q; want one of h264, h265, av1", codec)
	}
	if len(entries) == 0 {
		return nil, model.New(model.ErrIndexLength, codec, -1, "key-frame index is empty")
	}

	var prevEnd int64
	for i, e := range entries {
		at := e.Offset
		if e.Offset < 0 {
			return nil, model.New(model.ErrIndexOutOfRange, codec, at,
				"index entry %d has negative offset %d", i, e.Offset)
		}
		if e.Length <= 0 {
			return nil, model.New(model.ErrIndexLength, codec, at,
				"index entry %d has non-positive length %d", i, e.Length)
		}
		// Overflow-checked end offset.
		end, err := model.CheckedAdd(e.Offset, e.Length)
		if err != nil {
			return nil, model.New(model.ErrIndexOutOfRange, codec, at,
				"index entry %d offset+length overflows: %d+%d", i, e.Offset, e.Length).Wrap(err)
		}
		if streamLen >= 0 && end > streamLen {
			return nil, model.New(model.ErrIndexOutOfRange, codec, at,
				"index entry %d ends at %d beyond stream length %d", i, end, streamLen)
		}
		if !e.Key {
			return nil, model.New(model.ErrIndexOrder, codec, at,
				"index entry %d at %d is not marked as a key frame", i, e.Offset)
		}
		if i > 0 {
			if e.Offset < entries[i-1].Offset {
				return nil, model.New(model.ErrIndexOrder, codec, at,
					"index entry %d offset %d is not greater than previous offset %d",
					i, e.Offset, entries[i-1].Offset)
			}
			// Adjacent units (offset == prevEnd) are legal; only a start
			// inside the previous unit is an overlap.
			if e.Offset < prevEnd {
				return nil, model.New(model.ErrIndexOrder, codec, at,
					"index entry %d overlaps previous unit ending at %d", i, prevEnd)
			}
		}
		prevEnd = end
	}
	return &Index{Codec: codec, Entries: append([]IndexEntry(nil), entries...)}, nil
}

// ParseIndex is a convenience constructor used by the fixture and the command
// line: parallel offset/length/key slices become validated entries.
func ParseIndex(codec string, offsets, lengths []int64, keys []bool, streamLen int64) (*Index, error) {
	if len(offsets) != len(lengths) {
		return nil, model.New(model.ErrIndexLength, codec, -1,
			"offset/length slice length mismatch: %d vs %d", len(offsets), len(lengths))
	}
	entries := make([]IndexEntry, len(offsets))
	for i := range offsets {
		key := true
		if keys != nil {
			if i >= len(keys) {
				return nil, model.New(model.ErrIndexLength, codec, -1, "missing key flag at %d", i)
			}
			key = keys[i]
		}
		entries[i] = IndexEntry{Offset: offsets[i], Length: lengths[i], Key: key}
	}
	return ValidateIndex(codec, entries, streamLen)
}
