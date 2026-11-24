package container

import (
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"github.com/zld/mediarc/internal/model"
)

func validateAndParseBoxes(r io.ReadSeeker, size int64, cfg model.Config, f *File) error {
	trackCount := -1
	return walkBoxes(r, 0, size, 0, -1, &trackCount, cfg, f)
}

var containerBoxes = map[mp4.BoxType]bool{
	mp4.BoxTypeMoov(): true, mp4.BoxTypeTrak(): true, mp4.BoxTypeMdia(): true,
	mp4.BoxTypeMinf(): true, mp4.BoxTypeStbl(): true, mp4.BoxTypeEdts(): true,
	mp4.BoxTypeDinf(): true, mp4.BoxTypeUdta(): true, mp4.BoxTypeMoof(): true,
	mp4.BoxTypeTraf(): true, mp4.BoxTypeMvex(): true, mp4.BoxTypeIlst(): true,
}

func walkBoxes(r io.ReadSeeker, start, end int64, depth, trackIndex int, trackCount *int, cfg model.Config, f *File) error {
	if depth > cfg.Limits.MaxBoxDepth {
		return &model.ParseError{BoxType: "box-hierarchy", ByteOffset: start, Err: fmt.Errorf("box depth exceeds limit %d", cfg.Limits.MaxBoxDepth)}
	}
	pos := start
	for pos < end {
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return offsetError(pos, "seek", err)
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			return offsetError(pos, "box-header", err)
		}
		if int64(bi.Offset) != pos {
			pos = int64(bi.Offset)
		}
		headerEnd := pos + int64(bi.HeaderSize)
		if int64(bi.HeaderSize) <= 0 || headerEnd > end {
			return &model.ParseError{BoxType: bi.Type.String(), ByteOffset: pos, Err: errors.New("invalid box header size")}
		}
		boxEnd := pos + int64(bi.Size)
		if bi.Size == 0 && bi.ExtendToEOF {
			boxEnd = end
		}
		if bi.Size < bi.HeaderSize || boxEnd > end || boxEnd < headerEnd {
			return &model.ParseError{
				BoxType:    bi.Type.String(),
				ByteOffset: pos,
				Err:        fmt.Errorf("box size %d exceeds parent end %d", bi.Size, end),
			}
		}

		currentTrack := trackIndex
		if bi.Type == mp4.BoxTypeTrak() {
			*trackCount++
			currentTrack = *trackCount
		}
		rb := &RawBox{
			Type:       bi.Type.String(),
			Offset:     pos,
			Size:       int64(bi.Size),
			HeaderSize: int(bi.HeaderSize),
			Depth:      depth,
			TrackIndex: currentTrack,
		}
		if err := parsePayload(r, bi, pos, rb); err != nil {
			return err
		}
		f.Boxes = append(f.Boxes, rb)

		if containerBoxes[bi.Type] {
			if err := walkBoxes(r, headerEnd, boxEnd, depth+1, currentTrack, trackCount, cfg, f); err != nil {
				return err
			}
		}
		pos = boxEnd
	}
	return nil
}

func parsePayload(r io.ReadSeeker, bi *mp4.BoxInfo, offset int64, rb *RawBox) error {
	payloadSize := bi.Size - bi.HeaderSize
	if payloadSize == 0 {
		return nil
	}
	if _, err := bi.SeekToPayload(r); err != nil {
		return offsetError(offset, bi.Type.String(), err)
	}
	payload, _, err := mp4.UnmarshalAny(r, bi.Type, payloadSize, mp4.Context{})
	if err != nil {
		return &model.ParseError{BoxType: bi.Type.String(), ByteOffset: offset, Err: err}
	}
	rb.Payload = payload
	return nil
}

func offsetError(offset int64, box string, err error) error {
	return &model.ParseError{BoxType: box, ByteOffset: offset, Err: err}
}
