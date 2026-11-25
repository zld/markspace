package mediakernel

import (
	"bytes"
	"errors"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// MP4Meta summarizes a parsed MP4 container without retaining full box trees.
type MP4Meta struct {
	MajorBrand    string       `json:"major_brand"`
	MinorVersion  uint32       `json:"minor_version"`
	TopLevelBoxes []MP4BoxMeta `json:"top_level_boxes"`
	MdatSize      int64        `json:"mdat_size_bytes"`
	MdatPayload   int          `json:"mdat_payload_bytes"`
	MdatOffset    int64        `json:"mdat_offset"`
}

// MP4BoxMeta is one top-level box location, enough to verify encapsulation.
type MP4BoxMeta struct {
	Type   string `json:"type"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
}

// memFile is an in-memory io.WriteSeeker required by mp4.Writer, whose
// StartBox seeks backwards to patch box size fields once a box ends.
type memFile struct {
	buf []byte
	pos int
}

func (m *memFile) Write(p []byte) (int, error) {
	for _, b := range p {
		if m.pos < len(m.buf) {
			m.buf[m.pos] = b
		} else {
			m.buf = append(m.buf, b)
		}
		m.pos++
	}
	return len(p), nil
}

func (m *memFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		m.pos = int(offset)
	case io.SeekCurrent:
		m.pos += int(offset)
	case io.SeekEnd:
		m.pos = len(m.buf) + int(offset)
	}
	if m.pos < 0 || m.pos > len(m.buf) {
		return 0, errors.New("seek out of range")
	}
	return int64(m.pos), nil
}

// WrapInMP4 encapsulates payload (typically the framed packet stream) in a
// minimal valid MP4: ftyp + moov/mvhd + mdat. minorVersion changes only the
// encapsulation metadata; the mdat payload bytes are preserved exactly, which
// is the basis for the "container-only change" incremental scenario.
func WrapInMP4(payload []byte, minorVersion uint32) ([]byte, error) {
	out := &memFile{}
	w := mp4.NewWriter(out)

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, wrapMP4Err(-1, "start ftyp", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		MinorVersion:     minorVersion,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
	}, mp4.Context{}); err != nil {
		return nil, wrapMP4Err(0, "marshal ftyp", err)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, wrapMP4Err(0, "end ftyp", err)
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, wrapMP4Err(-1, "start moov", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return nil, wrapMP4Err(-1, "start mvhd", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Mvhd{
		Timescale: DefaultClockRate, DurationV0: 48000, NextTrackID: 2,
		Rate: 0x00010000, Volume: 0x0100,
	}, mp4.Context{}); err != nil {
		return nil, wrapMP4Err(-1, "marshal mvhd", err)
	}
	if _, err := w.EndBox(); err != nil { // mvhd
		return nil, wrapMP4Err(-1, "end mvhd", err)
	}
	if _, err := w.EndBox(); err != nil { // moov
		return nil, wrapMP4Err(-1, "end moov", err)
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, wrapMP4Err(-1, "start mdat", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Mdat{Data: payload}, mp4.Context{}); err != nil {
		return nil, wrapMP4Err(-1, "marshal mdat", err)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, wrapMP4Err(-1, "end mdat", err)
	}
	return out.buf, nil
}

func wrapMP4Err(offset int64, reason string, cause error) error {
	return NewError(PhaseParse, offset, "mp4: "+reason).WithCause(cause)
}

// UnwrapMP4 parses an MP4 container and returns the exact mdat payload plus
// box metadata. Truncation or malformed boxes produce structured errors that
// carry the byte offset where parsing failed.
func UnwrapMP4(raw []byte) ([]byte, *MP4Meta, error) {
	r := bytes.NewReader(raw)
	meta := &MP4Meta{}
	var mdatPayload []byte

	// Walk top-level boxes manually: this avoids nested expansion of mdat
	// payload bytes and gives each box's absolute offset for error reports.
	for {
		boxStart := r.Size() - int64(r.Len())
		bi, err := mp4.ReadBoxInfo(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, NewError(PhaseParse, boxStart, "mp4: malformed box header").WithCause(err)
		}
		offset := int64(bi.Offset)
		meta.TopLevelBoxes = append(meta.TopLevelBoxes, MP4BoxMeta{
			Type: bi.Type.String(), Offset: offset, Size: int64(bi.Size),
		})
		switch bi.Type {
		case mp4.BoxTypeFtyp():
			ftyp, perr := readFtyp(raw, bi)
			if perr != nil {
				return nil, nil, perr
			}
			meta.MajorBrand = string(bytes.Trim(ftyp.MajorBrand[:], "\x00"))
			meta.MinorVersion = ftyp.MinorVersion
		case mp4.BoxTypeMdat():
			payload, perr := readMdat(raw, bi)
			if perr != nil {
				return nil, nil, perr
			}
			mdatPayload = payload
			meta.MdatSize = int64(bi.Size)
			meta.MdatPayload = len(payload)
			meta.MdatOffset = int64(bi.Offset + bi.HeaderSize)
		}
		// Jump to the next top-level box regardless of its type.
		if _, err := r.Seek(int64(bi.Offset+bi.Size), io.SeekStart); err != nil {
			return nil, nil, NewError(PhaseParse, offset, "mp4: seek past box failed").WithCause(err)
		}
	}
	if len(mdatPayload) == 0 {
		return nil, nil, NewError(PhaseParse, 0, "mp4: no mdat payload found")
	}
	return mdatPayload, meta, nil
}

func readFtyp(raw []byte, bi *mp4.BoxInfo) (*mp4.Ftyp, error) {
	r := bytes.NewReader(raw)
	results, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{{mp4.BoxTypeFtyp()}})
	if err != nil {
		return nil, NewError(PhaseParse, int64(bi.Offset), "mp4: truncated ftyp").WithCause(err)
	}
	for _, res := range results {
		if f, ok := res.Payload.(*mp4.Ftyp); ok {
			return f, nil
		}
	}
	return nil, NewError(PhaseParse, int64(bi.Offset), "mp4: ftyp payload missing")
}

func readMdat(raw []byte, bi *mp4.BoxInfo) ([]byte, error) {
	r := bytes.NewReader(raw)
	results, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{{mp4.BoxTypeMdat()}})
	if err != nil {
		return nil, NewError(PhaseParse, int64(bi.Offset), "mp4: truncated mdat").WithCause(err)
	}
	for _, res := range results {
		if m, ok := res.Payload.(*mp4.Mdat); ok {
			return m.Data, nil
		}
	}
	return nil, NewError(PhaseParse, int64(bi.Offset), "mp4: mdat payload missing")
}
