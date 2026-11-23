package segment

import (
	"bytes"
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediadeliver/internal/model"
)

// maxMoofBytes bounds the one allocation per fragment. Real moof boxes are
// kilobytes; the limit only protects against corrupt size fields.
const maxMoofBytes = 16 << 20

// readMoof walks the moof children and extracts track defaults, decode
// times and sample counts. The moof payload is bounded by its declared size
// (already validated by the caller), so this never reads past the box.
func (s *scanner) readMoof(hdr boxHeader) error {
	payloadSize := hdr.size - hdr.headerSize
	if payloadSize > maxMoofBytes {
		s.res.Errors = append(s.res.Errors, model.NewError(model.CodeInvalidBoxSize, s.res.Path,
			hdr.offset, model.StageScan, true,
			fmt.Sprintf("moof payload of %d bytes exceeds the %d byte limit", payloadSize, maxMoofBytes)))
		return nil
	}
	if _, err := s.r.Seek(hdr.offset+hdr.headerSize, io.SeekStart); err != nil {
		return err
	}
	payload := make([]byte, payloadSize)
	if _, err := io.ReadFull(s.r, payload); err != nil {
		return err
	}
	reader := bytes.NewReader(payload)
	if _, err := mp4.ReadBoxStructure(reader, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeTraf():
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
		case mp4.BoxTypeTfhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			s.cur.TrackID = box.(*mp4.Tfhd).TrackID
		case mp4.BoxTypeTfdt():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			s.cur.BaseDecodeTime = box.(*mp4.Tfdt).GetBaseMediaDecodeTime()
		case mp4.BoxTypeTrun():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			trun := box.(*mp4.Trun)
			if uint32(len(trun.Entries)) != trun.SampleCount {
				return nil, fmt.Errorf("trun declares %d samples but carries %d entries", trun.SampleCount, len(trun.Entries))
			}
			s.cur.SampleCount += trun.SampleCount
			for _, e := range trun.Entries {
				s.cur.DurationTicks += uint64(e.SampleDuration)
			}
		}
		return nil, nil
	}); err != nil {
		// A malformed moof is recoverable: record it, attach it to the
		// pending boundary, and resynchronise at the next top-level box.
		s.res.Errors = append(s.res.Errors, model.NewError(model.CodeIncompleteSamples, s.res.Path,
			hdr.offset, model.StageScan, true, "moof sample table is incomplete: "+err.Error()))
		if s.cur != nil {
			s.cur.Corrupt = true
			s.cur.ErrorCode = string(model.CodeIncompleteSamples)
		}
		s.res.ScannedTo = hdr.offset
		return nil
	}
	if s.cur.SampleCount == 0 {
		s.res.Errors = append(s.res.Errors, model.NewError(model.CodeIncompleteSamples, s.res.Path,
			hdr.offset, model.StageScan, true, "moof declares no samples"))
		s.cur.Corrupt = true
		s.cur.ErrorCode = string(model.CodeIncompleteSamples)
	}
	return nil
}
