package mxf

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	auditerrors "media-audit/internal/errors"
	"media-audit/internal/media"
	"media-audit/internal/timebase"
)

type Key [16]byte

var (
	KeyPartition   = makeKey(0x01)
	KeyTrackMeta   = makeKey(0x10)
	KeySampleTable = makeKey(0x20)
	KeyIntegrity   = makeKey(0x30)
)

func makeKey(last byte) Key {
	return Key{0x06, 0x0e, 0x2b, 0x34, 0x04, 0x01, 0x01, 0x0d, 0x01, 0x03, 0x01, 0x01, 0x00, 0x00, 0x00, last}
}

func (k Key) Name() string {
	switch k {
	case KeyPartition:
		return "PartitionPack"
	case KeyTrackMeta:
		return "TrackMetadata"
	case KeySampleTable:
		return "AuditSampleTable"
	case KeyIntegrity:
		return "AuditIntegrity"
	default:
		return "UnknownUL:" + hex.EncodeToString(k[14:])
	}
}

type KLV struct {
	Key          Key
	KeyOffset    int64
	LengthOffset int64
	ValueOffset  int64
	HeaderSize   int
	Value        []byte
}

func readBER(data []byte, pos int) (int64, int, *auditerrors.Error) {
	if pos >= len(data) {
		return 0, 0, auditerrors.New(auditerrors.CodeTruncated, int64(len(data)), "missing BER length byte")
	}
	first := data[pos]
	if first < 0x80 {
		return int64(first), 1, nil
	}
	n := int(first & 0x7f)
	if first == 0x80 || n == 0 || n > 8 {
		return 0, 0, auditerrors.New(auditerrors.CodeInvalidLength, int64(pos), "unsupported BER length encoding 0x%02x", first)
	}
	if pos+1+n > len(data) {
		return 0, 0, auditerrors.New(auditerrors.CodeTruncated, int64(len(data)), "truncated %d-byte BER length", n)
	}
	var length int64
	for i := 0; i < n; i++ {
		length = length<<8 | int64(data[pos+1+i])
	}
	if length < 0 {
		return 0, 0, auditerrors.New(auditerrors.CodeInvalidLength, int64(pos), "BER length overflow")
	}
	return length, 1 + n, nil
}

// Parse decodes the project's recognizable SMPTE-UL KLV subset. It returns a
// partial document and traceable errors for damaged or unknown structures.
func Parse(path string, data []byte) *media.Document {
	doc := &media.Document{Format: media.FormatMXF, Path: path, Size: int64(len(data))}
	pos := 0
	var track *media.Track

	addError := func(e *auditerrors.Error) {
		doc.Errors = append(doc.Errors, e.WithPath(path))
	}

	for pos < len(data) {
		keyStart := int64(pos)
		if len(data)-pos < 16 {
			addError(auditerrors.New(auditerrors.CodeTruncated, int64(len(data)), "truncated KLV key").
				WithBytes(keyStart, int64(len(data))))
			break
		}
		var key Key
		copy(key[:], data[pos:pos+16])

		length, berLen, lerr := readBER(data, pos+16)
		if lerr != nil {
			lerr.Path = path
			if lerr.Bytes == nil {
				lerr.WithBytes(keyStart, int64(len(data)))
			}
			doc.Errors = append(doc.Errors, lerr)
			break
		}
		valueStart := int64(pos + 16 + berLen)
		valueEnd := valueStart + length
		if valueEnd > int64(len(data)) {
			addError(auditerrors.New(auditerrors.CodeTruncated, int64(len(data)),
				"KLV %s length %d exceeds available bytes", key.Name(), length).
				WithBytes(keyStart, int64(len(data))).
				WithExpectedActual("complete KLV value", "EOF"))
			break
		}

		value := data[valueStart:valueEnd]
		doc.Elements = append(doc.Elements, media.Element{
			Type:       key.Name(),
			Offset:     keyStart,
			HeaderSize: int(valueStart - keyStart),
			Range:      auditerrors.ByteRange{Start: keyStart, End: valueEnd},
		})

		switch key {
		case KeyTrackMeta:
			id, tb, duration, md, err := media.DecodeTrackMeta(value)
			if err != nil {
				addError(auditerrors.New(auditerrors.CodeUnknownStructure, valueStart, "bad track metadata: %v", err).
					WithBytes(keyStart, valueEnd))
				break
			}
			track = &media.Track{
				ID: id, Timebase: tb, Duration: duration, Metadata: md,
				ByteRange: auditerrors.ByteRange{Start: keyStart, End: valueEnd},
			}
			doc.Tracks = append(doc.Tracks, *track)
			track = &doc.Tracks[len(doc.Tracks)-1]

		case KeySampleTable:
			if track == nil {
				track = &media.Track{ID: 1, Timebase: timebase.MXF(25, 1)}
				doc.Tracks = append(doc.Tracks, *track)
				track = &doc.Tracks[len(doc.Tracks)-1]
			}
			samples, errs := media.ParseSampleTable(value, valueStart, track.Timebase, track.ID)
			track.Samples = samples
			track.ByteRange.End = valueEnd
			for _, e := range errs {
				addError(e)
			}

		case KeyIntegrity:
			integ, err := media.DecodeIntegrity(value, keyStart)
			if err != nil {
				addError(auditerrors.New(auditerrors.CodeUnknownStructure, valueStart, "bad integrity KLV: %v", err).
					WithBytes(keyStart, valueEnd))
				break
			}
			doc.Integrity = &integ

		case KeyPartition:
			// Recognized header; its payload is intentionally opaque.

		default:
			addError(auditerrors.New(auditerrors.CodeUnknownStructure, keyStart, "unknown MXF UL %s", key.Name()).
				WithBytes(keyStart, valueEnd))
		}

		pos = int(valueEnd)
	}

	for i := range doc.Tracks {
		finalizeTrack(&doc.Tracks[i], doc, path)
	}
	if encoded, err := json.Marshal(doc.Elements); err == nil {
		sum := sha256.Sum256(encoded)
		doc.StructureSHA256 = hex.EncodeToString(sum[:])
	}
	return doc
}

func finalizeTrack(t *media.Track, doc *media.Document, path string) {
	if err := t.Timebase.Validate(); err != nil {
		e := auditerrors.New(auditerrors.CodeInvalidTimestamp, t.ByteRange.Start, "track %d: %v", t.ID, err).
			WithPath(path).WithTrack(t.ID).WithBytes(t.ByteRange.Start, t.ByteRange.End)
		doc.Errors = append(doc.Errors, e)
		return
	}
	r, err := timebase.ConvertRange(t.Timebase, 0, t.Duration)
	if err != nil {
		e := auditerrors.New(auditerrors.CodeInvalidTimestamp, t.ByteRange.Start, "track %d duration: %v", t.ID, err).
			WithPath(path).WithTrack(t.ID)
		doc.Errors = append(doc.Errors, e)
		return
	}
	t.TimeRange = &r
}
