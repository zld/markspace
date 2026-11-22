package remux

import (
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// moov so nested structural boxes are validated, while mdat is detected but
// never parsed — codec payloads stay opaque.
func VerifyMP4(r io.ReadSeeker) (boxCount int, hasMdat bool, err error) {
	for {
		info, rerr := mp4.ReadBoxInfo(r)
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return 0, false, rerr
		}
		boxCount++
		if info.Type == mp4.BoxTypeMdat() {
			hasMdat = true
			if _, serr := r.Seek(int64(info.Size), io.SeekCurrent); serr != nil {
				return 0, false, serr
			}
			continue
		}
		if info.Type == mp4.BoxTypeMoov() {
			nested, eerr := countNested(r, info)
			if eerr != nil {
				return 0, false, eerr
			}
			boxCount += nested
		} else if _, serr := r.Seek(int64(info.HeaderSize), io.SeekCurrent); serr != nil {
			return 0, false, serr
		}
	}
	if boxCount == 0 {
		return 0, false, fmt.Errorf("remux: mp4 sidecar has no boxes")
	}
	return boxCount, hasMdat, nil
}

// countNested expands one box's children and returns the number of nested
// boxes visited, leaving the reader positioned at the box end.
func countNested(r io.ReadSeeker, parent *mp4.BoxInfo) (int, error) {
	end := int64(parent.Offset + parent.Size)
	n := 0
	for {
		pos, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, err
		}
		if pos >= end {
			break
		}
		info, err := mp4.ReadBoxInfo(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		n++
		if _, err := r.Seek(int64(info.Size-info.HeaderSize), io.SeekCurrent); err != nil {
			return 0, err
		}
	}
	if _, err := r.Seek(end, io.SeekStart); err != nil {
		return 0, err
	}
	return n, nil
}
