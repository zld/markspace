package demux

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediarc/internal/model"
)

const ArchiveBoxType = "MRCA"

type BoxSummary struct {
	Type      string `json:"type"`
	Offset    int64  `json:"offset"`
	Size      int64  `json:"size"`
	Supported bool   `json:"supported"`
}

type Container struct {
	Archive         *model.Archive `json:"archive"`
	Boxes           []BoxSummary   `json:"boxes"`
	Brand           string         `json:"brand"`
	GoMP4TrackCount int            `json:"go_mp4_track_count"`
}

// EncodeContainer writes a deterministic ISO-BMFF container. It includes a
// probe-compatible moov with standard track/sample tables, an mdat holding
// concatenated sample bytes, and the lossless MRCA manifest used by MediaRC.
func EncodeContainer(a *model.Archive) ([]byte, error) {
	brand := [4]byte{'i', 's', 'o', 'm'}
	if a.Brand == "MOV" {
		brand = [4]byte{'q', 't', ' ', ' '}
	}
	ftypPayload, err := fullBoxPayload(&mp4.Ftyp{
		MajorBrand:   brand,
		MinorVersion: 0x00000200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '2'}},
		},
	})
	if err != nil {
		return nil, model.NewError(model.StageDemux, model.CodeUnsupported, "marshal ftyp", err)
	}
	ftypBox := boxBytes(mp4.BoxTypeFtyp(), ftypPayload)

	zeroOffsets := make([]uint32, len(a.Tracks))
	moovZero, err := buildMoov(a, zeroOffsets)
	if err != nil {
		return nil, err
	}

	var mdatPayload bytes.Buffer
	offsets := make([]uint32, len(a.Tracks))
	mdatStart := len(ftypBox) + len(moovZero) + 8
	cursor := uint32(mdatStart)
	for i, tr := range a.Tracks {
		offsets[i] = cursor
		for _, s := range a.SamplesFor(tr.ID) {
			mdatPayload.Write(s.Payload)
			cursor += uint32(len(s.Payload))
		}
	}
	moovBox, err := buildMoov(a, offsets)
	if err != nil {
		return nil, err
	}
	if len(moovBox) != len(moovZero) {
		return nil, model.NewError(model.StageDemux, model.CodeNonLossless,
			"internal error: moov size changed while filling chunk offsets", nil)
	}
	mdatBox := boxBytes(mp4.BoxTypeMdat(), mdatPayload.Bytes())

	raw, err := json.Marshal(a)
	if err != nil {
		return nil, model.NewError(model.StageDemux, model.CodeUnsupported, "encode archive manifest", err)
	}
	mrcaBox := boxBytes(mp4.BoxType{'M', 'R', 'C', 'A'}, raw)

	var out bytes.Buffer
	out.Write(ftypBox)
	out.Write(moovBox)
	out.Write(mdatBox)
	out.Write(mrcaBox)
	return out.Bytes(), nil
}

// Demux scans box boundaries with go-mp4 and decodes the MRCA archive.
// Truncation and unsupported boxes always return a model.MediaError carrying
// the byte offset.
func Demux(data []byte) (*Container, error) {
	if len(data) < 8 {
		err := model.NewError(model.StageDemux, model.CodeTruncated, "container smaller than MP4 box header", io.ErrUnexpectedEOF)
		return nil, model.AtOffset(err, int64(len(data)))
	}

	// Probe independently validates the top-level ISO-BMFF structure where
	// go-mp4 has support. Our synthetic MRCA box is then decoded below.
	if info, err := mp4.Probe(bytes.NewReader(data)); err != nil {
		me := model.NewError(model.StageDemux, model.CodeTruncated, "go-mp4 probe failed", err)
		return nil, model.AtOffset(me, int64(len(data)))
	} else {
		_ = info
	}

	r := bytes.NewReader(data)
	c := &Container{}
	for {
		offset, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		if offset == int64(len(data)) {
			break
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			me := model.NewError(model.StageDemux, model.CodeTruncated, "cannot read MP4 box header", err)
			return nil, model.AtOffset(me, offset)
		}
		headerEnd, _ := r.Seek(0, io.SeekCurrent)
		if bi.Size < bi.HeaderSize || offset+int64(bi.Size) > int64(len(data)) {
			me := model.NewError(model.StageDemux, model.CodeTruncated,
				fmt.Sprintf("box %s declares size %d but only %d bytes remain",
					bi.Type.String(), bi.Size, int64(len(data))-offset), io.ErrUnexpectedEOF)
			return nil, model.AtOffset(me, int64(len(data)))
		}
		payloadSize := bi.Size - bi.HeaderSize
		payload := make([]byte, payloadSize)
		if _, err := io.ReadFull(r, payload); err != nil {
			me := model.NewError(model.StageDemux, model.CodeTruncated, "cannot read box payload", err)
			return nil, model.AtOffset(me, headerEnd)
		}
		c.Boxes = append(c.Boxes, BoxSummary{
			Type: bi.Type.String(), Offset: offset, Size: int64(bi.Size), Supported: true,
		})

		switch bi.Type {
		case mp4.BoxTypeFtyp():
			var ftyp mp4.Ftyp
			if _, err := mp4.Unmarshal(bytes.NewReader(payload), payloadSize, &ftyp, mp4.Context{}); err != nil {
				me := model.NewError(model.StageDemux, model.CodeCorruptPayload, "invalid ftyp", err)
				return nil, model.AtOffset(me, offset)
			}
			c.Brand = string(ftyp.MajorBrand[:])
		case mp4.BoxTypeMoov(), mp4.BoxTypeMdat():
			// Standard movie/sample boxes are validated by mp4.Probe;
			// MRCA carries complete payloads for lossless verification.
		case mp4.BoxType{'M', 'R', 'C', 'A'}:
			var a model.Archive
			if err := json.Unmarshal(payload, &a); err != nil {
				off := headerEnd
				if se, ok := err.(*json.SyntaxError); ok {
					off = headerEnd + se.Offset
				}
				me := model.NewError(model.StageDemux, model.CodeTruncated, "cannot decode MRCA archive", err)
				return nil, model.AtOffset(me, off)
			}
			for i := range a.Events {
				if err := ParseEvent(&a.Events[i]); err != nil {
					return nil, err
				}
			}
			c.Archive = &a
		default:
			me := model.NewError(model.StageDemux, model.CodeUnsupported,
				fmt.Sprintf("critical box %s is not declared supported", bi.Type.String()), nil)
			return nil, model.AtOffset(me, offset)
		}
		if _, err := r.Seek(offset+int64(bi.Size), io.SeekStart); err != nil {
			return nil, err
		}
	}
	if c.Archive == nil {
		err := model.NewError(model.StageDemux, model.CodeUnsupported, "container has no MRCA archive box", nil)
		return nil, model.AtOffset(err, 0)
	}
	if info, _ := mp4.Probe(bytes.NewReader(data)); info != nil {
		c.GoMP4TrackCount = len(info.Tracks)
	}
	return c, nil
}

// Truncate returns a deterministic prefix and never panics on an invalid n.
func Truncate(data []byte, n int) []byte {
	if n < 0 {
		n = 0
	}
	if n > len(data) {
		n = len(data)
	}
	return append([]byte(nil), data[:n]...)
}
