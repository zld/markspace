package mp4box

import (
	"bytes"
	"encoding/json"
	"io"

	"avpipe/internal/model"
	"github.com/abema/go-mp4"
)

// MvhdTime holds the time fields parsed from the mvhd box.
type MvhdTime struct {
	Timescale uint32
	Duration  uint32
}

// Read parses the flat container, returning the avpi descriptor and mvhd time
// fields. Unknown box types fall back to mp4.AnyTypeBox so the custom avpi box
// is still surfaced through the standard go-mp4 traversal.
func Read(r io.ReadSeeker) (model.CaseDesc, MvhdTime, error) {
	var (
		desc model.CaseDesc
		mv   MvhdTime
		avpi []byte
	)
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.StrToBoxType(model.AVPIBox):
			var buf bytes.Buffer
			if _, err := h.ReadData(&buf); err != nil {
				return nil, err
			}
			avpi = buf.Bytes()
			return &mp4.AnyTypeBox{Type: h.BoxInfo.Type}, nil
		case mp4.BoxTypeMvhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if m, ok := box.(*mp4.Mvhd); ok {
				mv.Timescale = m.Timescale
				mv.Duration = m.DurationV0
			}
			return box, nil
		default:
			// Recurse into containers (moov) and skip the rest.
			return h.Expand()
		}
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		return desc, mv, model.NewError(model.CodeIO, false, "mp4 parse: %v", err)
	}
	if len(avpi) == 0 {
		return desc, mv, model.NewError(model.CodeIO, false, "missing %s box", model.AVPIBox)
	}
	if err := json.Unmarshal(avpi, &desc); err != nil {
		return desc, mv, model.NewError(model.CodeIO, false, "descriptor json: %v", err)
	}
	return desc, mv, nil
}
