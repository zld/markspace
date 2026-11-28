package container

import (
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// children lists the boxes directly inside the extent [start, end).
func children(r io.ReadSeeker, start, end int64) ([]mp4.BoxInfo, error) {
	boxes := make([]mp4.BoxInfo, 0, 8)
	offset := start
	for offset+mp4.SmallHeaderSize <= end {
		if _, err := r.Seek(offset, io.SeekStart); err != nil {
			return boxes, err
		}
		info, err := mp4.ReadBoxInfo(r)
		if err != nil {
			return boxes, err
		}
		boxEnd := int64(info.Offset + info.Size)
		if info.Size < info.HeaderSize || boxEnd > end {
			return boxes, fmt.Errorf("box %s at %d overruns its parent", info.Type, info.Offset)
		}
		boxes = append(boxes, *info)
		offset = boxEnd
	}
	return boxes, nil
}

// findFirst returns the first child with the given type.
func findFirst(boxes []mp4.BoxInfo, boxType mp4.BoxType) (mp4.BoxInfo, bool) {
	for _, box := range boxes {
		if box.Type == boxType {
			return box, true
		}
	}
	return mp4.BoxInfo{}, false
}

// parseBox decodes a box payload into dst.
func parseBox(r io.ReadSeeker, info mp4.BoxInfo, dst mp4.IBox) error {
	if _, err := info.SeekToPayload(r); err != nil {
		return err
	}
	_, err := mp4.Unmarshal(r, info.Size-info.HeaderSize, dst, mp4.Context{})
	return err
}
