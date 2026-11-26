package adapter

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"

	"continuity/internal/core"
	"continuity/internal/manifest"
)

// MP4Parser parses fMP4/CMAF segments via go-mp4: moof offset, tfdt base
// decode time, trun sample durations and an mdat content fingerprint.
type MP4Parser struct{}

// Version implements ParserV1.
func (MP4Parser) Version() int { return 1 }

// ParseSegment implements ParserV1. Truncated boxes are reported with the
// byte offset of the offending box; the timeline is never silently
// shortened.
func (MP4Parser) ParseSegment(r io.ReadSeeker, source string) (*SegmentInfo, error) {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, core.NewError(core.StageParse, core.CodeInput, source, 0, -1, "seek", err)
	}
	if size > manifest.MaxSegmentBytes {
		return nil, core.NewError(core.StageParse, core.CodeOversize, source, 0, -1,
			fmt.Sprintf("size=%d limit=%d", size, manifest.MaxSegmentBytes), nil)
	}
	if err := checkTruncation(r, source, size); err != nil {
		return nil, err
	}
	info := &SegmentInfo{Source: source, Size: size, MoofOffset: -1}
	hash := sha256.New()
	var walk mp4.ReadHandler
	walk = func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoof():
			info.MoofOffset = int64(h.BoxInfo.Offset)
			if _, err := h.Expand(walk); err != nil {
				return nil, core.NewError(core.StageParse, core.CodeTruncatedBox, source, 0,
					info.MoofOffset, "moof payload", err)
			}
		case mp4.BoxTypeTraf():
			if _, err := h.Expand(walk); err != nil {
				return nil, core.NewError(core.StageParse, core.CodeTruncatedBox, source, 0,
					int64(h.BoxInfo.Offset), "traf payload", err)
			}
		case mp4.BoxTypeTfhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			info.TrackID = box.(*mp4.Tfhd).TrackID
		case mp4.BoxTypeTfdt():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			info.BaseTime = int64(box.(*mp4.Tfdt).GetBaseMediaDecodeTime())
		case mp4.BoxTypeTrun():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			for _, e := range box.(*mp4.Trun).Entries {
				info.Samples = append(info.Samples, Sample{Duration: int64(e.SampleDuration)})
			}
		case mp4.BoxTypeMdat():
			if _, err := h.ReadData(hash); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(r, walk); err != nil {
		if ce, ok := err.(*core.Error); ok {
			return nil, ce
		}
		return nil, core.NewError(core.StageParse, core.CodeInput, source, 0, -1, "box walk", err)
	}
	copy(info.Fingerprint[:], hash.Sum(nil))
	if info.MoofOffset < 0 {
		return nil, core.NewError(core.StageParse, core.CodeInput, source, 0, -1, "no moof box", nil)
	}
	return info, nil
}

// checkTruncation scans top-level box headers and verifies each declared
// size fits inside the file, so a truncated box is blamed on its own byte
// offset before go-mp4 sees the file.
func checkTruncation(r io.ReadSeeker, source string, size int64) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	for off := int64(0); off < size; {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return core.NewError(core.StageParse, core.CodeTruncatedBox, source, 0, off,
				"box header truncated", err)
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		hdrSize := int64(8)
		switch boxSize {
		case 1:
			var ext [8]byte
			if _, err := io.ReadFull(r, ext[:]); err != nil {
				return core.NewError(core.StageParse, core.CodeTruncatedBox, source, 0, off,
					"extended size truncated", err)
			}
			boxSize = int64(binary.BigEndian.Uint64(ext[:]))
			hdrSize = 16
		case 0:
			boxSize = size - off
		}
		if boxSize < hdrSize || off+boxSize > size {
			return core.NewError(core.StageParse, core.CodeTruncatedBox, source, 0, off,
				fmt.Sprintf("box %s size=%d exceeds file size=%d", typ, boxSize, size), nil)
		}
		off += boxSize
		if _, err := r.Seek(off, io.SeekStart); err != nil {
			return err
		}
	}
	_, err := r.Seek(0, io.SeekStart)
	return err
}
