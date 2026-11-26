package manifest

import (
	"bytes"

	"github.com/abema/go-mp4"

	"archivecheck/internal"
)

// FMP4Info summarizes boxes decoded by go-mp4.
type FMP4Info struct {
	Boxes    []string `json:"boxes"`
	HasMoof  bool     `json:"has_moof"`
	HasMdat  bool     `json:"has_mdat"`
	Complete bool     `json:"complete"`
}

// InspectFMP4 uses go-mp4 to read an fMP4 fragment and rejects truncated or
// structurally corrupt bytes with a sourced fault.
func InspectFMP4(data []byte, source string) (*FMP4Info, error) {
	if len(data) < 8 {
		return nil, internal.NewFault("fmp4.parse", source, 0, "fragment is shorter than an MP4 box header")
	}
	info := &FMP4Info{Complete: true}
	reader := bytes.NewReader(data)
	_, err := mp4.ReadBoxStructure(reader, func(h *mp4.ReadHandle) (any, error) {
		name := h.BoxInfo.Type.String()
		info.Boxes = append(info.Boxes, name)
		switch name {
		case "moof":
			info.HasMoof = true
		case "mdat":
			info.HasMdat = true
		}
		if h.BoxInfo.Offset+h.BoxInfo.Size > uint64(len(data)) {
			info.Complete = false
			return nil, internal.NewFault("fmp4.truncated", source, int(h.BoxInfo.Offset),
				"box %s size %d exceeds fragment length %d", name, h.BoxInfo.Size, len(data))
		}
		if _, err := h.Expand(); err != nil {
			return nil, internal.WrapFault("fmp4.parse", source, int(h.BoxInfo.Offset), err)
		}
		return nil, nil
	})
	if err != nil {
		return nil, internal.WrapFault("fmp4.parse", source, 0, err)
	}
	if !info.Complete || !info.HasMoof || !info.HasMdat {
		return nil, internal.NewFault("fmp4.parse", source, 0,
			"fragment incomplete: moof=%v mdat=%v complete=%v", info.HasMoof, info.HasMdat, info.Complete)
	}
	return info, nil
}
