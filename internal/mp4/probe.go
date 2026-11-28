package mp4

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"mediapipe/internal/model"
)

// probeMetadata is the primary parse path: it uses go-mp4 to decode box
// structures, track headers, media headers and edit lists. It runs only after
// the bounded scanner has accepted the box layout, so go-mp4 never sees a
// length that could make it read past the declared input.
type probeMetadata struct {
	trackID   uint32
	handler   string
	timescale uint32
	edits     []EditEntry
}

// readProbe decodes the metadata of every trak under moov with go-mp4.
func readProbe(data []byte) (map[string]probeMetadata, error) {
	r := bytes.NewReader(data)
	traks, err := gomp4.ExtractBoxes(r, nil, []gomp4.BoxPath{
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak()},
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]probeMetadata, len(traks))
	for _, trak := range traks {
		tid, err := extractTkhd(r, trak)
		if err != nil {
			return nil, err
		}
		meta := probeMetadata{trackID: tid}
		if err := fillMdia(r, trak, &meta); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%d@%d", tid, trak.Offset)
		out[key] = meta
	}
	return out, nil
}

func extractTkhd(r io.ReadSeeker, trak *gomp4.BoxInfo) (uint32, error) {
	boxes, err := gomp4.ExtractBoxWithPayload(r, trak, gomp4.BoxPath{gomp4.BoxTypeTkhd()})
	if err != nil {
		return 0, err
	}
	if len(boxes) == 0 {
		return 0, errors.New("trak has no tkhd")
	}
	tkhd, ok := boxes[0].Payload.(*gomp4.Tkhd)
	if !ok {
		return 0, errors.New("tkhd payload has unexpected type")
	}
	return tkhd.TrackID, nil
}

func fillMdia(r io.ReadSeeker, trak *gomp4.BoxInfo, meta *probeMetadata) error {
	mdhdBoxes, err := gomp4.ExtractBoxWithPayload(r, trak,
		gomp4.BoxPath{gomp4.BoxTypeMdia(), gomp4.BoxTypeMdhd()})
	if err != nil {
		return err
	}
	if len(mdhdBoxes) == 0 {
		return errors.New("trak has no mdhd")
	}
	mdhd, ok := mdhdBoxes[0].Payload.(*gomp4.Mdhd)
	if !ok {
		return errors.New("mdhd payload has unexpected type")
	}
	meta.timescale = mdhd.Timescale

	hdlrBoxes, err := gomp4.ExtractBoxWithPayload(r, trak,
		gomp4.BoxPath{gomp4.BoxTypeMdia(), gomp4.BoxTypeHdlr()})
	if err != nil {
		return err
	}
	if len(hdlrBoxes) > 0 {
		if hdlr, ok := hdlrBoxes[0].Payload.(*gomp4.Hdlr); ok {
			meta.handler = string(hdlr.HandlerType[:])
		}
	}

	elstBoxes, err := gomp4.ExtractBoxWithPayload(r, trak,
		gomp4.BoxPath{gomp4.BoxTypeEdts(), gomp4.BoxTypeElst()})
	if err != nil {
		return err
	}
	if len(elstBoxes) > 0 {
		elst, ok := elstBoxes[0].Payload.(*gomp4.Elst)
		if ok {
			meta.edits = editsFromElst(elst)
		}
	}
	return nil
}

func editsFromElst(elst *gomp4.Elst) []EditEntry {
	out := make([]EditEntry, 0, len(elst.Entries))
	for i := range elst.Entries {
		out = append(out, EditEntry{
			Duration:  int64(elst.GetSegmentDuration(i)),
			MediaTime: elst.GetMediaTime(i),
			Rate:      int64(elst.Entries[i].MediaRateInteger),
		})
	}
	return out
}

// readProbeFromStream reads the whole payload once so the go-mp4 reader sees
// exactly the bytes the bounded scanner already validated.
func readProbeFromStream(s *Stream) (map[string]probeMetadata, error) {
	if s.size > int64(^uint(0)>>1) {
		return nil, model.New(model.KindInvalid, model.StageMP4Scan, s.asset, "probe",
			errors.New("input too large for metadata probe"))
	}
	data := make([]byte, s.size)
	if s.size > 0 {
		if err := s.readAt(data, 0); err != nil {
			return nil, err
		}
	}
	// The bounded scanner owns layout validation: it knows the exact byte
	// offset of the offending box, which go-mp4's errors do not carry. Run it
	// first so a corrupt container is reported precisely, and only decode
	// metadata once the layout is known good.
	if err := s.validateLayout(); err != nil {
		return nil, err
	}
	if _, err := decodeAllBoxes(data); err != nil {
		return nil, s.corrupt("probe", 0, fmt.Errorf("go-mp4 rejected validated layout: %w", err))
	}
	meta, err := readProbe(data)
	if err != nil {
		return nil, s.corrupt("probe", 0, err)
	}
	return meta, nil
}

// decodeAllBoxes asks go-mp4 to walk the top-level box tree. It is the
// library's own structural validation, used alongside our bounded scanner.
func decodeAllBoxes(data []byte) ([]gomp4.BoxPath, error) {
	r := bytes.NewReader(data)
	var paths []gomp4.BoxPath
	handler := func(h *gomp4.ReadHandle) (interface{}, error) {
		paths = append(paths, h.Path)
		if len(h.Path) < 4 {
			_, err := h.Expand()
			return nil, err
		}
		return nil, nil
	}
	if _, err := gomp4.ReadBoxStructure(r, handler); err != nil {
		return nil, err
	}
	return paths, nil
}
