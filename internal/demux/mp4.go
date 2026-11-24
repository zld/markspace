package demux

import (
	"bytes"
	"encoding/binary"
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

// EncodeContainer writes a deterministic ISO-BMFF-style container. The MRCA
// box carries the self-describing synthetic archive; ftyp is encoded with
// go-mp4. Sample bytes are retained exactly in the archive payload.
func EncodeContainer(a *model.Archive) ([]byte, error) {
	var out bytes.Buffer
	brand := [4]byte{'i', 's', 'o', 'm'}
	if a.Brand == "MOV" {
		brand = [4]byte{'q', 't', ' ', ' '}
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   brand,
		MinorVersion: 0x00000200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '2'}},
		},
	}
	var fp bytes.Buffer
	if _, err := mp4.Marshal(&fp, ftyp, mp4.Context{}); err != nil {
		return nil, model.NewError(model.StageDemux, model.CodeUnsupported, "marshal ftyp", err)
	}
	if err := writeBox(&out, mp4.BoxTypeFtyp(), fp.Bytes()); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, model.NewError(model.StageDemux, model.CodeUnsupported, "encode archive manifest", err)
	}
	if err := writeBox(&out, mp4.BoxType{'M', 'R', 'C', 'A'}, raw); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeBox(w *bytes.Buffer, typ mp4.BoxType, payload []byte) error {
	size := uint32(8 + len(payload))
	if err := binary.Write(w, binary.BigEndian, size); err != nil {
		return err
	}
	w.Write(typ[:])
	_, err := w.Write(payload)
	return err
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
