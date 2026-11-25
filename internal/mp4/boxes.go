package mp4

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"

	gomp4 "github.com/abema/go-mp4"

	auditerrors "media-audit/internal/errors"
	"media-audit/internal/media"
	"media-audit/internal/timebase"
)

type box struct {
	typ          string
	start        int64
	headerSize   int
	payloadStart int64
	end          int64
}

var containers = map[string]bool{"moov": true, "trak": true, "mdia": true, "minf": true, "stbl": true}

var knownBoxes = map[string]bool{
	"ftyp": true, "moov": true, "mvhd": true, "trak": true, "tkhd": true,
	"mdia": true, "mdhd": true, "hdlr": true, "minf": true, "stbl": true,
	"stsd": true, "avc1": true, "avcC": true, "clap": true, "colr": true,
	"mdat": true, "free": true, "wide": true, "amet": true, "asmp": true,
	"adig": true,
}

func scanBoxes(data []byte, start, end int64, errs *[]*auditerrors.Error) []box {
	var boxes []box
	pos := start
	for pos < end {
		if end-pos < 8 {
			eof := int64(len(data))
			*errs = append(*errs, auditerrors.New(auditerrors.CodeTruncated, eof, "truncated MP4 box header").
				WithBytes(pos, eof))
			return boxes
		}
		size32 := binary.BigEndian.Uint32(data[pos : pos+4])
		typ := string(data[pos+4 : pos+8])
		header := int64(8)
		size := int64(size32)
		if size32 == 1 {
			if end-pos < 16 {
				eof := int64(len(data))
				*errs = append(*errs, auditerrors.New(auditerrors.CodeTruncated, eof, "truncated 64-bit box size").
					WithBytes(pos, eof))
				return boxes
			}
			size = int64(binary.BigEndian.Uint64(data[pos+8 : pos+16]))
			header = 16
		} else if size32 == 0 {
			size = end - pos
		}
		if size < header {
			*errs = append(*errs, auditerrors.New(auditerrors.CodeInvalidLength, pos, "box %s size %d < header %d", typ, size, header))
			return boxes
		}
		boxEnd := pos + size
		if boxEnd > end || boxEnd > int64(len(data)) {
			eof := int64(len(data))
			*errs = append(*errs, auditerrors.New(auditerrors.CodeTruncated, eof, "box %s declared end %d past EOF", typ, boxEnd).
				WithBytes(pos, eof).WithExpectedActual("complete box", "EOF"))
			return boxes
		}
		b := box{typ: typ, start: pos, headerSize: int(header), payloadStart: pos + header, end: boxEnd}
		boxes = append(boxes, b)
		if containers[typ] {
			boxes = append(boxes, scanBoxes(data, b.payloadStart, b.end, errs)...)
		}
		pos = boxEnd
	}
	return boxes
}

func findBox(boxes []box, typ string) *box {
	for i := range boxes {
		if boxes[i].typ == typ {
			return &boxes[i]
		}
	}
	return nil
}

func payload(data []byte, b *box) []byte {
	return data[b.payloadStart:b.end]
}

// Parse uses a deterministic local box scan for exact offsets and also drives
// github.com/abema/go-mp4 through ReadBoxStructure for standards-level parsing.
func Parse(path string, data []byte) *media.Document {
	doc := &media.Document{Format: media.FormatMP4, Path: path, Size: int64(len(data))}
	scanErrs := []*auditerrors.Error{}
	boxes := scanBoxes(data, 0, int64(len(data)), &scanErrs)
	for _, e := range scanErrs {
		doc.Errors = append(doc.Errors, e.WithPath(path))
	}
	for _, b := range boxes {
		if !knownBoxes[b.typ] {
			doc.Errors = append(doc.Errors, auditerrors.New(
				auditerrors.CodeUnknownStructure, b.start, "unrecognized MP4 box %s", b.typ,
			).WithPath(path).WithBytes(b.start, b.end))
		}
	}
	for _, b := range boxes {
		doc.Elements = append(doc.Elements, media.Element{
			Type: b.typ, Offset: b.start, HeaderSize: b.headerSize,
			Range: auditerrors.ByteRange{Start: b.start, End: b.end},
		})
	}

	track := media.Track{ID: 1, Timebase: timebase.MP4(90000)}
	if b := findBox(boxes, "tkhd"); b != nil {
		p := payload(data, b)
		if len(p) >= 84 && p[0] == 0 {
			track.ID = binary.BigEndian.Uint32(p[12:16])
			track.Duration = int64(binary.BigEndian.Uint32(p[20:24]))
			ma := int32(binary.BigEndian.Uint32(p[40:44]))
			mb := int32(binary.BigEndian.Uint32(p[44:48]))
			if ma != 0 || mb != 0 {
				angle := math.Atan2(float64(mb)/65536.0, float64(ma)/65536.0) * 180 / math.Pi
				track.Metadata.HasRotation = true
				track.Metadata.RotationDegrees = int32(math.Round(angle))
			}
			w := binary.BigEndian.Uint32(p[76:80]) >> 16
			h := binary.BigEndian.Uint32(p[80:84]) >> 16
			track.ByteRange = auditerrors.ByteRange{Start: b.start, End: b.end}
			parseCleanAperture(data, boxes, w, h, &track)
		}
	}
	if b := findBox(boxes, "mdhd"); b != nil {
		p := payload(data, b)
		if len(p) >= 24 && p[0] == 0 {
			ts := binary.BigEndian.Uint32(p[12:16])
			track.Timebase = timebase.MP4(ts)
			track.Duration = int64(binary.BigEndian.Uint32(p[16:20]))
		}
	}
	if b := findBox(boxes, "colr"); b != nil {
		if c, err := parseColor(payload(data, b)); err == nil {
			track.Metadata.Color = c
		} else {
			addParseError(doc, path, b, "colr", err)
		}
	}
	if b := findBox(boxes, "amet"); b != nil {
		if id, tb, dur, md, err := media.DecodeTrackMeta(payload(data, b)); err == nil {
			if id != 0 {
				track.ID = id
			}
			track.Metadata.MetadataVersion = md.MetadataVersion
			if !track.Metadata.HasRotation && md.HasRotation {
				track.Metadata.HasRotation = true
				track.Metadata.RotationDegrees = md.RotationDegrees
			}
			if track.Metadata.Crop == nil {
				track.Metadata.Crop = md.Crop
			}
			if track.Metadata.Color == nil {
				track.Metadata.Color = md.Color
			}
			if dur != 0 {
				track.Duration = dur
			}
			if tb.Numerator != 0 {
				track.Timebase = tb
			}
		} else {
			addParseError(doc, path, b, "amet", err)
		}
	}
	if b := findBox(boxes, "asmp"); b != nil {
		samples, errs := media.ParseSampleTable(payload(data, b), b.payloadStart, track.Timebase, track.ID)
		track.Samples = samples
		track.ByteRange.End = b.end
		for _, e := range errs {
			doc.Errors = append(doc.Errors, e.WithPath(path))
		}
	}
	if b := findBox(boxes, "adig"); b != nil {
		if integ, err := media.DecodeIntegrity(payload(data, b), b.start); err == nil {
			doc.Integrity = &integ
		} else {
			addParseError(doc, path, b, "adig", err)
		}
	}
	if err := track.Timebase.Validate(); err == nil {
		if r, err := timebase.ConvertRange(track.Timebase, 0, track.Duration); err == nil {
			track.TimeRange = &r
		}
	}
	doc.Tracks = []media.Track{track}

	if _, err := gomp4.ReadBoxStructure(bytes.NewReader(data), func(h *gomp4.ReadHandle) (any, error) {
		switch h.BoxInfo.Type.String() {
		case "moov", "trak", "mdia", "minf", "stbl":
			return h.Expand()
		default:
			return nil, nil
		}
	}); err != nil && len(doc.Errors) == 0 {
		doc.Errors = append(doc.Errors, auditerrors.New(auditerrors.CodeUnknownStructure, 0, "go-mp4: %v", err).WithPath(path))
	}

	if encoded, err := json.Marshal(doc.Elements); err == nil {
		sum := sha256.Sum256(encoded)
		doc.StructureSHA256 = hex.EncodeToString(sum[:])
	}
	return doc
}

func addParseError(doc *media.Document, path string, b *box, typ string, err error) {
	doc.Errors = append(doc.Errors, auditerrors.New(auditerrors.CodeUnknownStructure, b.payloadStart, "bad %s: %v", typ, err).
		WithPath(path).WithBytes(b.start, b.end))
}

func parseColor(p []byte) (*media.Color, error) {
	if len(p) < 11 || string(p[:4]) != "nclx" {
		return nil, errors.New("unsupported colour box")
	}
	return &media.Color{
		Primaries: binary.BigEndian.Uint16(p[4:6]),
		Transfer:  binary.BigEndian.Uint16(p[6:8]),
		Matrix:    binary.BigEndian.Uint16(p[8:10]),
		FullRange: p[10]&0x80 != 0,
	}, nil
}

func parseCleanAperture(data []byte, boxes []box, encodedW, encodedH uint32, track *media.Track) {
	b := findBox(boxes, "clap")
	if b == nil || len(payload(data, b)) < 32 {
		return
	}
	p := payload(data, b)
	cw := int64(binary.BigEndian.Uint32(p[0:4])) / int64(binary.BigEndian.Uint32(p[4:8]))
	ch := int64(binary.BigEndian.Uint32(p[8:12])) / int64(binary.BigEndian.Uint32(p[12:16]))
	if encodedW == 0 || encodedH == 0 || cw <= 0 || ch <= 0 {
		return
	}
	xMargins := (int64(encodedW) - cw) / 2
	yMargins := (int64(encodedH) - ch) / 2
	track.Metadata.Crop = &media.Crop{Top: yMargins, Bottom: yMargins, Left: xMargins, Right: xMargins}
}
