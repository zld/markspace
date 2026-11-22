// Package container parses MP4/MOV box structure and track time bases using
// github.com/abema/go-mp4, and validates sample tables against file bounds.
package container

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"
)

// Limits bound resource use while parsing. All are configurable and must be
// positive; ParseFile rejects inputs that exceed them.
type Limits struct {
	MaxFileSize int64
	MaxSamples  uint32
	MaxTracks   int
}

// DefaultLimits are the pipeline defaults.
func DefaultLimits() Limits {
	return Limits{MaxFileSize: 1 << 34, MaxSamples: 1 << 22, MaxTracks: 64}
}

// BoxError pinpoints a structural failure: track (0 = file level), box type
// and byte offset. Parsing of the current stage aborts on this error.
type BoxError struct {
	TrackID uint32
	BoxType string
	Offset  uint64
	Msg     string
}

func (e *BoxError) Error() string {
	return fmt.Sprintf("container: track %d box %q at offset %d: %s",
		e.TrackID, e.BoxType, e.Offset, e.Msg)
}

// TrackInfo carries the parsed time base and raw sample tables of one track.
type TrackInfo struct {
	TrackID      uint32
	Timescale    uint32
	Duration     uint64
	Kind         string // "video", "audio" or "other"
	Stts         []mp4.SttsEntry
	Stsc         []mp4.StscEntry
	SampleSize   uint32   // stsz uniform size (0 => per-sample EntrySize)
	EntrySize    []uint32 // stsz per-sample sizes
	ChunkOffsets []uint64 // stco widened to 64 bit, or co64
}

// FileInfo is the parsed container.
type FileInfo struct {
	Path     string
	Size     int64
	MajorBrand string
	Tracks   []TrackInfo
}

// containerTypes are boxes whose children we descend into during the
// bounds pre-scan.
var containerTypes = map[[4]byte]bool{
	{'m', 'o', 'o', 'v'}: true, {'t', 'r', 'a', 'k'}: true,
	{'m', 'd', 'i', 'a'}: true, {'m', 'i', 'n', 'f'}: true,
	{'s', 't', 'b', 'l'}: true, {'d', 'i', 'n', 'f'}: true,
	{'e', 'd', 't', 's'}: true, {'u', 'd', 't', 'a'}: true,
	{'m', 'o', 'o', 'f'}: true, {'t', 'r', 'a', 'f'}: true,
}

// scanBounds walks every box header and rejects truncated headers and boxes
// extending beyond end of file, reporting type and offset.
func scanBounds(r io.ReaderAt, fileSize int64) error {
	var walk func(off, end int64) error
	walk = func(off, end int64) error {
		for off < end {
			var hdr [8]byte
			if _, err := r.ReadAt(hdr[:], off); err != nil {
				return &BoxError{BoxType: "????", Offset: uint64(off),
					Msg: "truncated box header"}
			}
			size := uint64(binary.BigEndian.Uint32(hdr[0:4]))
			typ := [4]byte{hdr[4], hdr[5], hdr[6], hdr[7]}
			headerSize := uint64(8)
			if size == 1 {
				var ext [8]byte
				if _, err := r.ReadAt(ext[:], off+8); err != nil {
					return &BoxError{BoxType: string(typ[:]), Offset: uint64(off),
						Msg: "truncated extended box header"}
				}
				size = binary.BigEndian.Uint64(ext[:])
				headerSize = 16
			} else if size == 0 {
				size = uint64(end - off) // box extends to end of parent
			}
			if size < headerSize {
				return &BoxError{BoxType: string(typ[:]), Offset: uint64(off),
					Msg: fmt.Sprintf("invalid box size %d", size)}
			}
			if size > uint64(end-off) {
				return &BoxError{BoxType: string(typ[:]), Offset: uint64(off),
					Msg: fmt.Sprintf("box size %d exceeds remaining %d bytes", size, end-off)}
			}
			if containerTypes[typ] {
				if err := walk(off+int64(headerSize), off+int64(size)); err != nil {
					return err
				}
			}
			off += int64(size)
		}
		return nil
	}
	return walk(0, fileSize)
}

// ParseFile opens path, enforces limits, pre-scans box bounds, then extracts
// track time bases and sample tables with go-mp4.
func ParseFile(path string, lim Limits) (*FileInfo, error) {
	if lim.MaxFileSize <= 0 || lim.MaxSamples == 0 || lim.MaxTracks <= 0 {
		return nil, fmt.Errorf("container: limits must be positive")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > lim.MaxFileSize {
		return nil, &BoxError{BoxType: "file", Offset: 0,
			Msg: fmt.Sprintf("file size %d exceeds limit %d", st.Size(), lim.MaxFileSize)}
	}
	if err := scanBounds(f, st.Size()); err != nil {
		return nil, err
	}
	fi := &FileInfo{Path: path, Size: st.Size()}
	if err := fi.extract(f, lim); err != nil {
		return nil, err
	}
	return fi, nil
}

// leafPaths are the boxes extracted per track, relative to a trak box.
var leafPaths = []mp4.BoxPath{
	{mp4.BoxTypeTkhd()},
	{mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
	{mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()},
	{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStts()},
	{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsc()},
	{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsz()},
	{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStco()},
	{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeCo64()},
}

// extract pulls moov/trak structure via go-mp4 and assembles TrackInfo.
func (fi *FileInfo) extract(r io.ReadSeeker, lim Limits) error {
	moovs, err := mp4.ExtractBoxes(r, nil, []mp4.BoxPath{{mp4.BoxTypeMoov()}})
	if err != nil {
		return &BoxError{BoxType: "moov", Offset: 0, Msg: err.Error()}
	}
	if len(moovs) == 0 {
		return &BoxError{BoxType: "moov", Offset: 0, Msg: "missing moov box"}
	}
	traks, err := mp4.ExtractBoxes(r, moovs[0], []mp4.BoxPath{{mp4.BoxTypeTrak()}})
	if err != nil {
		return &BoxError{BoxType: "trak", Offset: moovs[0].Offset, Msg: err.Error()}
	}
	if len(traks) == 0 {
		return &BoxError{BoxType: "trak", Offset: moovs[0].Offset, Msg: "no tracks"}
	}
	if len(traks) > lim.MaxTracks {
		return &BoxError{BoxType: "trak", Offset: moovs[0].Offset,
			Msg: fmt.Sprintf("track count %d exceeds limit %d", len(traks), lim.MaxTracks)}
	}
	for _, trak := range traks {
		t, err := extractTrack(r, trak)
		if err != nil {
			return err
		}
		fi.Tracks = append(fi.Tracks, *t)
	}
	return nil
}

func extractTrack(r io.ReadSeeker, trak *mp4.BoxInfo) (*TrackInfo, error) {
	boxes, err := mp4.ExtractBoxesWithPayload(r, trak, leafPaths)
	if err != nil {
		return nil, &BoxError{BoxType: "trak", Offset: trak.Offset, Msg: err.Error()}
	}
	t := &TrackInfo{Kind: "other"}
	for _, b := range boxes {
		switch p := b.Payload.(type) {
		case *mp4.Tkhd:
			t.TrackID = p.TrackID
		case *mp4.Mdhd:
			t.Timescale = p.Timescale
			t.Duration = p.DurationV1
			if t.Duration == 0 {
				t.Duration = uint64(p.DurationV0)
			}
		case *mp4.Hdlr:
			switch string(p.HandlerType[:]) {
			case "vide":
				t.Kind = "video"
			case "soun":
				t.Kind = "audio"
			}
		case *mp4.Stts:
			t.Stts = p.Entries
		case *mp4.Stsc:
			t.Stsc = p.Entries
		case *mp4.Stsz:
			t.SampleSize = p.SampleSize
			t.EntrySize = p.EntrySize
		case *mp4.Stco:
			for _, o := range p.ChunkOffset {
				t.ChunkOffsets = append(t.ChunkOffsets, uint64(o))
			}
		case *mp4.Co64:
			t.ChunkOffsets = append(t.ChunkOffsets, p.ChunkOffset...)
		}
	}
	if t.Timescale == 0 {
		return nil, &BoxError{TrackID: t.TrackID, BoxType: "mdhd",
			Offset: trak.Offset, Msg: "zero or missing timescale"}
	}
	return t, nil
}
