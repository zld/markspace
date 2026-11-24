// Package mp4v1 is the explicitly registered major version 1 MP4 parser.
package mp4v1

import (
	"io"

	gomp4 "github.com/abema/go-mp4"

	"medpipe/internal/adapter"
	"medpipe/internal/clock"
)

const MajorVersion = 1

type Decoder struct{}

func init() {
	if err := adapter.Register(Decoder{}); err != nil {
		panic(err)
	}
}

func (Decoder) ParserMajor() int { return MajorVersion }
func (Decoder) Format() string   { return "mp4" }

// Decode parses top-level ISO-BMFF boxes with go-mp4. All library failures are
// converted to clock.Error values; adapter errors never touch the timeline
// directly as arbitrary errors.
func (d Decoder) Decode(r io.ReadSeeker, base *clock.Location) (*adapter.Document, error) {
	if r == nil {
		return nil, clock.Errorf(clock.CodeMissingField, withMajor(base), "missing MP4 reader")
	}

	boxes := make([]adapter.Box, 0, 8)
	_, err := gomp4.ReadBoxStructure(r, func(h *gomp4.ReadHandle) (any, error) {
		boxes = append(boxes, adapter.Box{
			Type:   h.BoxInfo.Type.String(),
			Offset: h.BoxInfo.Offset,
			Size:   h.BoxInfo.Size,
		})
		return nil, nil
	})
	if err != nil {
		code := clock.CodeInvalidPacket
		loc := withMajor(base)
		if base != nil {
			cp := *base
			loc = &cp
		}
		return nil, clock.Wrap(code, loc, err, "illegal or truncated MP4 box length")
	}
	if len(boxes) == 0 {
		return nil, clock.Errorf(clock.CodeMissingField, withMajor(base), "MP4 contains no boxes")
	}

	var hasFtyp, hasMdat bool
	for _, b := range boxes {
		switch b.Type {
		case "ftyp":
			hasFtyp = true
		case "mdat":
			hasMdat = true
		}
	}
	if !hasFtyp {
		return nil, clock.Errorf(clock.CodeMissingField, withMajor(base), "MP4 missing required ftyp box")
	}
	if !hasMdat {
		return nil, clock.Errorf(clock.CodeMissingField, withMajor(base), "MP4 missing required mdat box")
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, clock.Wrap(clock.CodeInvalidPacket, withMajor(base), err, "rewind MP4")
	}
	info, err := gomp4.Probe(r)
	if err != nil {
		return nil, clock.Wrap(clock.CodeInvalidPacket, withMajor(base), err, "MP4 probe failed")
	}
	if info.MajorBrand == [4]byte{} {
		return nil, clock.Errorf(clock.CodeMissingField, withMajor(base), "MP4 ftyp missing major_brand")
	}

	return &adapter.Document{
		ParserMajor: MajorVersion,
		Format:      d.Format(),
		Boxes:       boxes,
		Segments:    len(info.Segments),
	}, nil
}

func withMajor(loc *clock.Location) *clock.Location {
	if loc == nil {
		return &clock.Location{ParserMajor: MajorVersion}
	}
	cp := *loc
	cp.ParserMajor = MajorVersion
	return &cp
}

var _ adapter.Decoder = Decoder{}
