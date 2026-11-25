package media

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/zld/media-regress/internal/error"
	"github.com/zld/media-regress/internal/mxfschema"
)

// MXF parsing targets the bounded regression profile emitted by
// fixture/mxf.go: a single header partition (Preface, description and index
// KLVs in local-set encoding) followed by a run of essence KLVs keyed with
// mxfschema.VideoEssence / mxfschema.AudioEssence. Anything outside this profile is rejected
// with a structured probe or metadata error rather than reinterpreted.

// klvEntry is one parsed KLV triplet.
type klvEntry struct {
	Key      mxfschema.UL
	Value    []byte
	Offset   int64 // offset of the 16-byte key
	ValueOff int64 // offset of the first value byte
	End      int64
}

func (e klvEntry) is(k mxfschema.UL) bool { return e.Key == k }

// readBER decodes a BER length at buf[0:]; it returns the length and the
// number of bytes consumed, or an error for an unsupported form.
func readBER(buf []byte) (int, int, error) {
	if len(buf) == 0 {
		return 0, 0, fmt.Errorf("empty ber")
	}
	b := buf[0]
	if b < 0x80 {
		return int(b), 1, nil
	}
	n := int(b & 0x7f)
	if n == 0 || n > 8 {
		return 0, 0, fmt.Errorf("unsupported ber length form 0x%02x", b)
	}
	if len(buf) < 1+n {
		return 0, 0, fmt.Errorf("truncated ber length")
	}
	var v uint64
	for i := 0; i < n; i++ {
		v = v<<8 | uint64(buf[1+i])
	}
	return int(v), 1 + n, nil
}

// parseKLV walks a byte range of KLV triplets and returns the entries.
func parseKLV(data []byte, base int64) ([]klvEntry, error) {
	var out []klvEntry
	pos := 0
	for pos < len(data) {
		if len(data)-pos < 17 {
			return nil, merr.New(merr.StageProbe, merr.KindTruncated, 0, base+int64(pos),
				"truncated mxf klv header (%d bytes left)", len(data)-pos)
		}
		var key mxfschema.UL
		copy(key[:], data[pos:pos+16])
		length, n, err := readBER(data[pos+16:])
		if err != nil {
			return nil, merr.Wrap(merr.StageProbe, merr.KindParse, 0, base+int64(pos+16), err, "mxf klv length")
		}
		valueOff := pos + 16 + n
		if length < 0 || valueOff+length > len(data) {
			return nil, merr.New(merr.StageProbe, merr.KindTruncated, 0, base+int64(valueOff),
				"mxf klv value overruns input (%d bytes, %d available)", length, len(data)-valueOff)
		}
		out = append(out, klvEntry{
			Key: key, Value: data[valueOff : valueOff+length],
			Offset: base + int64(pos), ValueOff: base + int64(valueOff),
			End: base + int64(valueOff+length),
		})
		pos = valueOff + length
	}
	return out, nil
}

// localSetField is one tag/length/value entry in a local set.
type localSetField struct {
	Tag   uint16
	Value []byte
	Off   int64 // absolute offset of the value bytes
}

// parseLocalSet decodes a flat local set. Duplicate tags are retained so the
// caller can reject ambiguous descriptors.
func parseLocalSet(data []byte, base int64) ([]localSetField, error) {
	var out []localSetField
	pos := 0
	for pos < len(data) {
		if len(data)-pos < 3 {
			return nil, merr.New(merr.StageMetadata, merr.KindTruncated, 0, base+int64(pos),
				"truncated local set tag")
		}
		tag := uint16(data[pos])<<8 | uint16(data[pos+1])
		length, n, err := readBER(data[pos+2:])
		if err != nil {
			return nil, merr.Wrap(merr.StageMetadata, merr.KindParse, 0, base+int64(pos+2), err,
				"local set tag 0x%04x length", tag)
		}
		valueOff := pos + 2 + n
		if valueOff+length > len(data) {
			return nil, merr.New(merr.StageMetadata, merr.KindTruncated, 0, base+int64(valueOff),
				"local set tag 0x%04x value overruns set", tag)
		}
		out = append(out, localSetField{Tag: tag, Value: data[valueOff : valueOff+length], Off: base + int64(valueOff)})
		pos = valueOff + length
	}
	return out, nil
}

// field returns the single field with tag, rejecting duplicates.
func field(fields []localSetField, tag uint16, track uint32, offset int64) (*localSetField, error) {
	var found *localSetField
	for i := range fields {
		if fields[i].Tag != tag {
			continue
		}
		if found != nil {
			return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, track, fields[i].Off,
				"duplicate local set tag 0x%04x", tag)
		}
		found = &fields[i]
	}
	if found == nil {
		return nil, merr.New(merr.StageMetadata, merr.KindParse, track, offset,
			"missing local set tag 0x%04x", tag)
	}
	return found, nil
}

func (f *localSetField) uintValue(track uint32) (uint64, error) {
	switch len(f.Value) {
	case 1:
		return uint64(f.Value[0]), nil
	case 2:
		return uint64(binary.BigEndian.Uint16(f.Value)), nil
	case 4:
		return uint64(binary.BigEndian.Uint32(f.Value)), nil
	case 8:
		return binary.BigEndian.Uint64(f.Value), nil
	default:
		return 0, merr.New(merr.StageMetadata, merr.KindParse, track, f.Off,
			"tag 0x%04x has unsupported integer width %d", f.Tag, len(f.Value))
	}
}

func (f *localSetField) stringValue() string { return string(f.Value) }

// probeMXF reports whether data starts with an MXF partition key.
func probeMXF(data []byte) bool {
	return len(data) >= 16 && bytes.Equal(data[:16], mxfschema.HeaderPartition[:])
}

// mxPartition describes the parsed header partition and core sets.
type mxPartition struct {
	preface     klvEntry
	descriptors []klvEntry
	index       klvEntry
	essence     []klvEntry
	totalSize   uint64
}

// parseMXFPartition validates the header partition and collects core sets.
func parseMXFPartition(data []byte) (*mxPartition, error) {
	entries, err := parseKLV(data, 0)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 || !entries[0].is(mxfschema.HeaderPartition) {
		return nil, merr.New(merr.StageProbe, merr.KindParse, 0, 0, "mxf: missing header partition key")
	}
	packFields, err := parseLocalSet(entries[0].Value, entries[0].ValueOff)
	if err != nil {
		return nil, err
	}
	part := &mxPartition{}
	if f, err := field(packFields, mxfschema.TagPartitionSize, 0, entries[0].ValueOff); err == nil {
		part.totalSize, _ = f.uintValue(0)
	}
	for _, e := range entries[1:] {
		switch {
		case e.is(mxfschema.Preface):
			if part.preface.Value != nil {
				return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, 0, e.Offset,
					"mxf: duplicate preface")
			}
			part.preface = e
		case e.is(mxfschema.Descriptor):
			part.descriptors = append(part.descriptors, e)
		case e.is(mxfschema.Index):
			if part.index.Value != nil {
				return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, 0, e.Offset,
					"mxf: duplicate index")
			}
			part.index = e
		case e.is(mxfschema.VideoEssence), e.is(mxfschema.AudioEssence):
			part.essence = append(part.essence, e)
		default:
			return nil, merr.New(merr.StageMetadata, merr.KindParse, 0, e.Offset,
				"mxf: unsupported core set key %x", e.Key[:4])
		}
	}
	if part.preface.Value == nil {
		return nil, merr.New(merr.StageMetadata, merr.KindParse, 0, 0, "mxf: missing preface")
	}
	if len(part.descriptors) == 0 {
		return nil, merr.New(merr.StageMetadata, merr.KindParse, 0, 0, "mxf: missing descriptors")
	}
	if part.index.Value == nil {
		return nil, merr.New(merr.StageMetadata, merr.KindParse, 0, 0, "mxf: missing index")
	}
	return part, nil
}
