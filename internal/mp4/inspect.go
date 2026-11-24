// Package mp4 inspects local MP4 files using go-mp4. It extracts the moov
// essentials the kernel needs (movie header, per-track edit lists, timescale,
// duration and sparse sync samples) and reports structural damage with the
// exact byte offset that failed.
package mp4

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	mp4 "github.com/abema/go-mp4"

	"mediasync/internal/model"
)

// TrackInfo is one track as read from an MP4 movie box.
type TrackInfo struct {
	ID            uint32
	Handler       string
	Timescale     uint32
	DurationTicks uint64
	Duration      model.Nanos
	Edit          []model.Interval // in media timescale order, kernel units
	RawEdits      []mp4.ElstEntry
	Keyframes     []uint64 // 1-based sample numbers from stss
	KeyframeTimes []model.Nanos
	MediaTime     int64 // first media_time of the edit list, if present
	StartOffset   model.Nanos
}

// Info is the inspected result for one file.
type Info struct {
	Path          string
	Size          int64
	Timescale     uint32
	DurationTicks uint64
	Duration      model.Nanos
	Tracks        []TrackInfo
	Diagnostics   model.ErrorList
}

// ErrTruncated is reported when a box header claims more bytes than the file
// holds. The accompanying model.Error carries the offending byte offset.
var ErrTruncated = errors.New("mp4 truncated")

// InspectFile opens and inspects a local MP4.
func InspectFile(path string) (*Info, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, model.NewError(model.CodeMP4Structure, model.Loc{File: path}, "stat mp4").Wrap(err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, model.NewError(model.CodeMP4Structure, model.Loc{File: path}, "open mp4").Wrap(err)
	}
	defer f.Close()
	return Inspect(f, path, st.Size())
}

// Inspect walks the top-level box structure and decodes the metadata boxes
// the kernel consumes. Size is the expected file size, which lets the walker
// distinguish a truncated file from an unreadable one.
func Inspect(r io.ReadSeeker, path string, size int64) (*Info, error) {
	info := &Info{Path: path, Size: size}
	var moov *mp4.BoxInfo

	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		b := h.BoxInfo
		end := b.Offset + b.Size
		if b.Size < mp4.SmallHeaderSize {
			info.Diagnostics.Add(model.NewError(model.CodeMP4Truncated,
				model.Loc{File: path, ByteOffset: int64(b.Offset), HasByteOffset: true},
				"box %q has size %d smaller than a box header", b.Type.String(), b.Size))
			return nil, stopWalk
		}
		if b.Size > uint64(size) || end > uint64(size) {
			info.Diagnostics.Add(model.NewError(model.CodeMP4Truncated,
				model.Loc{File: path, ByteOffset: int64(b.Offset), HasByteOffset: true},
				"box %q at offset %d claims %d bytes but file ends at %d", b.Type.String(), b.Offset, b.Size, size))
			return nil, stopWalk
		}
		switch b.Type {
		case mp4.BoxTypeMoov():
			bi := b
			moov = &bi
			return nil, nil
		case mp4.BoxTypeFtyp():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if _, ok := box.(*mp4.Ftyp); !ok {
				return nil, fmt.Errorf("unexpected ftyp payload %T", box)
			}
		}
		return nil, nil
	})
	if err != nil && !errors.Is(err, stopWalk) {
		info.Diagnostics.Add(locate(err, path, r))
	}
	if moov == nil {
		if len(info.Diagnostics) == 0 {
			info.Diagnostics.Add(model.NewError(model.CodeMP4Structure,
				model.Loc{File: path, ByteOffset: size, HasByteOffset: true},
				"file has no moov box (offset %d)", size))
		}
		return info, info.Diagnostics
	}
	if err := inspectMoov(r, path, moov, info); err != nil {
		info.Diagnostics.Add(locate(err, path, r))
	}
	sort.SliceStable(info.Diagnostics, func(i, j int) bool {
		return info.Diagnostics[i].Loc.ByteOffset < info.Diagnostics[j].Loc.ByteOffset
	})
	if len(info.Diagnostics) > 0 {
		return info, info.Diagnostics
	}
	return info, nil
}

var stopWalk = errors.New("stop walk")

// locate converts an error from go-mp4 into a structured diagnostic with the
// closest recoverable byte offset. The read cursor is already positioned at
// the failing box header in most cases, so that offset anchors the report.
func locate(err error, path string, r io.ReadSeeker) *model.Error {
	off, _ := r.Seek(0, io.SeekCurrent)
	code := model.CodeMP4Structure
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		code = model.CodeMP4Truncated
	}
	loc := model.Loc{File: path, HasByteOffset: true, ByteOffset: off}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return model.NewError(code, loc, "unexpected end of file inside box structure").Wrap(err)
	}
	return model.NewError(code, loc, "cannot decode box structure").Wrap(err)
}
