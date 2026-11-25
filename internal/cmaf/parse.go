// Package cmaf parses CMAF/fMP4 init segments and media fragments,
// validating box structure and reporting truncation with file offsets.
package cmaf

import (
	"fmt"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"replay/internal/model"
)

// Limits guarding against corrupt or hostile inputs.
const (
	MaxBoxSize     = 1 << 31 // no single box may exceed 2 GiB
	MaxTrunSamples = 1 << 20 // sanity bound on samples per trun
	MaxBoxes       = 1 << 16 // sanity bound on boxes per file
)

// TrackType classifies a track in the init segment.
type TrackType string

const (
	TrackVideo TrackType = "video"
	TrackAudio TrackType = "audio"
)

// TruncError reports a box whose declared size runs past available data.
type TruncError struct {
	Offset   int64  // file offset of the box header
	BoxType  string // empty when the header itself is cut
	Declared int64  // declared box size, 0 when the header is cut
	Remain   int64  // bytes actually available from Offset
}

func (e *TruncError) Error() string {
	if e.BoxType == "" {
		return fmt.Sprintf("truncated box header at offset %d (%d bytes remain)", e.Offset, e.Remain)
	}
	return fmt.Sprintf("truncated box %s at offset %d: declared %d bytes, %d remain",
		e.BoxType, e.Offset, e.Declared, e.Remain)
}

// Unwrap exposes model.ErrTruncated for errors.Is checks.
func (e *TruncError) Unwrap() error { return model.ErrTruncated }

// TrackInfo describes one track declared in the init segment.
type TrackInfo struct {
	TrackID   uint32
	Type      TrackType
	Timescale uint32
	Width     uint32
	Height    uint32
	Audio     *model.AudioInfo
	Color     *model.ColorInfo // nil when the input carries no color metadata
}

// Init is the parsed init segment.
type Init struct {
	Path   string
	Tracks []TrackInfo
}

// Video returns the first video track, or nil.
func (i *Init) Video() *TrackInfo { return i.trackOfType(TrackVideo) }

// Audio returns the first audio track, or nil.
func (i *Init) Audio() *TrackInfo { return i.trackOfType(TrackAudio) }

func (i *Init) trackOfType(t TrackType) *TrackInfo {
	for k := range i.Tracks {
		if i.Tracks[k].Type == t {
			return &i.Tracks[k]
		}
	}
	return nil
}

// TrackTypeOf resolves a track ID to its type.
func (i *Init) TrackTypeOf(id uint32) TrackType {
	for k := range i.Tracks {
		if i.Tracks[k].TrackID == id {
			return i.Tracks[k].Type
		}
	}
	return ""
}

// Fragment is one parsed media fragment (moof+mdat).
type Fragment struct {
	Path     string
	Seq      uint32 // mfhd sequence number
	Tfdt     uint64 // video track base media decode time (video timescale)
	VideoDur uint64 // video decode duration (video timescale)
	HasVideo bool
	AudioTfdt uint64
	AudioDur  uint64
	HasAudio  bool
	Damaged   bool // box structure truncated; metadata may be partial
	FileSize  int64
}

// Parser parses files and records structural issues into Report.
type Parser struct {
	Report *model.Report
}

// NewParser returns a Parser writing issues to rep (a new one if nil).
func NewParser(rep *model.Report) *Parser {
	if rep == nil {
		rep = &model.Report{}
	}
	return &Parser{Report: rep}
}

var containerBoxes = map[mp4.BoxType]bool{
	mp4.BoxTypeMoov(): true,
	mp4.BoxTypeTrak(): true,
	mp4.BoxTypeMdia(): true,
	mp4.BoxTypeMinf(): true,
	mp4.BoxTypeStbl(): true,
	mp4.BoxTypeMoof(): true,
	mp4.BoxTypeTraf(): true,
}

// walk iterates boxes in [base, end), recursing into known containers.
// fn is called for every box with its nesting depth (0 = top level)
// before descending into it.
func walk(f io.ReadSeeker, base, end int64, depth int, fn func(bi *mp4.BoxInfo, depth int) error) error {
	off := base
	n := 0
	for off < end {
		if end-off < 8 {
			return &TruncError{Offset: off, Remain: end - off}
		}
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return err
		}
		bi, err := mp4.ReadBoxInfo(f)
		if err != nil {
			return &TruncError{Offset: off, Remain: end - off}
		}
		size := int64(bi.Size)
		if bi.ExtendToEOF {
			size = end - off
		}
		if size < int64(bi.HeaderSize) || size > MaxBoxSize || off+size > end {
			return &TruncError{
				Offset:   int64(bi.Offset),
				BoxType:  bi.Type.String(),
				Declared: int64(bi.Size),
				Remain:   end - off,
			}
		}
		n++
		if n > MaxBoxes {
			return fmt.Errorf("cmaf: more than %d boxes, refusing to continue", MaxBoxes)
		}
		if err := fn(bi, depth); err != nil {
			return err
		}
		if containerBoxes[bi.Type] {
			if err := walk(f, off+int64(bi.HeaderSize), off+size, depth+1, fn); err != nil {
				return err
			}
		}
		off += size
	}
	return nil
}

// unmarshalBox reads the payload of bi into box using go-mp4.
func unmarshalBox(f io.ReadSeeker, bi *mp4.BoxInfo, box mp4.IBox) error {
	if _, err := f.Seek(int64(bi.Offset)+int64(bi.HeaderSize), io.SeekStart); err != nil {
		return err
	}
	_, err := mp4.Unmarshal(f, bi.Size-bi.HeaderSize, box, mp4.Context{})
	return err
}

// readPayload returns the payload bytes of bi, bounded by MaxBoxSize.
func readPayload(f io.ReadSeeker, bi *mp4.BoxInfo) ([]byte, error) {
	size := bi.Size - bi.HeaderSize
	if size > MaxBoxSize {
		return nil, fmt.Errorf("cmaf: box %s payload %d exceeds limit", bi.Type, size)
	}
	if _, err := f.Seek(int64(bi.Offset)+int64(bi.HeaderSize), io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, &TruncError{Offset: int64(bi.Offset), BoxType: bi.Type.String(),
			Declared: int64(bi.Size), Remain: 0}
	}
	return buf, nil
}

// openFile opens path for reading; callers must close it.
func openFile(path string) (*os.File, error) {
	return os.Open(path)
}

// fileSize returns the size of f, or 0 if it cannot be determined.
func fileSize(f *os.File) int64 {
	st, err := f.Stat()
	if err != nil {
		return 0
	}
	return st.Size()
}

// WalkBoxes enumerates every box in path, invoking fn with each BoxInfo.
// Truncation is reported as *TruncError carrying the file offset.
func (p *Parser) WalkBoxes(path string, fn func(bi *mp4.BoxInfo) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	return walk(f, 0, st.Size(), 0, func(bi *mp4.BoxInfo, _ int) error { return fn(bi) })
}

// TopLevelBoxes returns the top-level boxes of path in file order.
func (p *Parser) TopLevelBoxes(path string) ([]mp4.BoxInfo, error) {
	var out []mp4.BoxInfo
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	err = walk(f, 0, st.Size(), 0, func(bi *mp4.BoxInfo, depth int) error {
		if depth == 0 {
			out = append(out, *bi)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
