// Package media parses unfragmented, unencrypted MP4/MOV containers and
// turns the raw sample tables into per-track timelines expressed in the
// tracks' native time bases. No average frame rate is ever used.
package media

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"replay/internal/clock"
)

// HandlerType identifies a track by its handler box, not by box heuristics.
type HandlerType string

const (
	HandlerVideo HandlerType = "vide"
	HandlerAudio HandlerType = "soun"
)

// RawBox keeps the on-disk bytes of a box so the cutter can copy sample
// descriptions verbatim without understanding every codec-specific field.
type RawBox struct {
	Type   string
	Offset int64
	Size   int64
	Data   []byte // full box including its header
}

// TrackBoxes collects the parsed sample tables of one track.
type TrackBoxes struct {
	TrackID  uint32
	Handler  HandlerType
	Timescale uint32
	MDHDDur  uint64
	Width    uint16
	Height   uint16
	Volume   uint16

	Stts *mp4.Stts
	Ctts *mp4.Ctts
	Stsc *mp4.Stsc
	Stsz *mp4.Stsz
	Stco *mp4.Stco
	Co64 *mp4.Co64
	Stss *mp4.Stss
	// StsdEntry is the first sample entry (avc1/mp4a ...) copied raw.
	StsdEntry *RawBox
}

// FileInfo is everything the scan stage learns from the container.
type FileInfo struct {
	Path       string
	Size       int64
	TopLevel   []BoxRef
	Moov       BoxRef
	Mdat       BoxRef
	Mvhd       *mp4.Mvhd
	Tracks     []*TrackBoxes
	SourceSHA  [32]byte // filled by the pipeline, kept here for convenience
}

// BoxRef is a top-level box location; offsets are absolute file bytes.
type BoxRef struct {
	Type   string
	Offset int64
	Size   int64
	Header int64
}

// headerLen reads an 8/16 byte box header at the current position.
func headerLen(htype string, size32 uint32) int {
	if htype == "uuid" {
		return 16 + 16
	}
	if size32 == 1 {
		return 16
	}
	return 8
}

// scanTopLevel walks the top-level box chain manually so that a truncated
// header or payload is reported with the exact byte offset of the box.
func scanTopLevel(f *os.File) ([]BoxRef, BoxRef, BoxRef, error) {
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, BoxRef{}, BoxRef{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, BoxRef{}, BoxRef{}, err
	}
	var refs []BoxRef
	var moov, mdat BoxRef
	pos := int64(0)
	hdr := make([]byte, 16)
	for pos < size {
		if _, err := io.ReadFull(f, hdr[:8]); err != nil {
			return nil, BoxRef{}, BoxRef{}, clock.NewError(clock.KindBoxTruncated,
				"scan", "box header truncated at EOF").WithOffset(pos)
		}
		size32 := binary.BigEndian.Uint32(hdr[0:4])
		htype := string(hdr[4:8])
		hlen := headerLen(htype, size32)
		if hlen == 16 {
			if _, err := io.ReadFull(f, hdr[8:16]); err != nil {
				return nil, BoxRef{}, BoxRef{}, clock.NewError(clock.KindBoxTruncated,
					"scan", "extended box header truncated").WithOffset(pos)
			}
		}
		var boxSize int64
		switch {
		case size32 == 1:
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:16]))
		case size32 == 0:
			boxSize = size - pos
		default:
			boxSize = int64(size32)
		}
		if boxSize < int64(hlen) {
			return nil, BoxRef{}, BoxRef{}, clock.NewError(clock.KindBoxMalformed,
				"scan", fmt.Sprintf("box %s size %d smaller than header", htype, boxSize)).WithOffset(pos)
		}
		if pos+boxSize > size {
			return nil, BoxRef{}, BoxRef{}, clock.NewError(clock.KindBoxTruncated,
				"scan", fmt.Sprintf("box %s payload runs past EOF", htype)).WithOffset(pos)
		}
		ref := BoxRef{Type: htype, Offset: pos, Size: boxSize, Header: int64(hlen)}
		refs = append(refs, ref)
		switch htype {
		case "moov":
			moov = ref
		case "mdat":
			mdat = ref
		}
		pos += boxSize
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return nil, BoxRef{}, BoxRef{}, err
		}
	}
	return refs, moov, mdat, nil
}

// readStsdEntry extracts the first sample entry as raw bytes, including
// its header and nested avcC/esds payload.
func readStsdEntry(f *os.File, bi *mp4.BoxInfo) (*RawBox, error) {
	if _, err := f.Seek(int64(bi.Offset+bi.HeaderSize), io.SeekStart); err != nil {
		return nil, err
	}
	var head [8]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return nil, clock.NewError(clock.KindBoxTruncated, "stsd",
			"sample description header truncated").WithOffset(int64(bi.Offset))
	}
	// stsd full box: version/flags (4) then entry_count (4).
	count := binary.BigEndian.Uint32(head[4:8])
	if count == 0 {
		return nil, clock.NewError(clock.KindBoxMalformed, "stsd", "no sample entry")
	}
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return nil, clock.NewError(clock.KindBoxTruncated, "stsd",
			"sample entry header truncated").WithOffset(int64(bi.Offset))
	}
	entryOff, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	entryOff -= 8
	entrySize := int64(binary.BigEndian.Uint32(head[0:4]))
	entryEnd := int64(bi.Offset) + int64(bi.Size)
	if entrySize < 8 || entryOff+entrySize > entryEnd {
		return nil, clock.NewError(clock.KindBoxTruncated, "stsd",
			fmt.Sprintf("sample entry size %d out of bounds", entrySize)).WithOffset(entryOff)
	}
	raw := make([]byte, entrySize)
	if _, err := f.Seek(entryOff, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(f, raw); err != nil {
		return nil, clock.NewError(clock.KindBoxTruncated, "stsd",
			"sample entry payload truncated").WithOffset(entryOff)
	}
	return &RawBox{
		Type:   string(head[4:8]),
		Offset: entryOff,
		Size:   entrySize,
		Data:   raw,
	}, nil
}

var stblBoxTypes = map[string]bool{
	"stts": true, "ctts": true, "stsc": true, "stsz": true,
	"stco": true, "co64": true, "stss": true,
}

// parseMoov walks moov/trak/mdia/minf/stbl with the mp4 library and
// captures the sample tables needed for timeline construction and cutting.
func parseMoov(f *os.File, moov BoxRef) (*mp4.Mvhd, []*TrackBoxes, error) {
	section := io.NewSectionReader(f, moov.Offset, moov.Size)
	root := &mp4.BoxInfo{Offset: uint64(moov.Offset), Size: uint64(moov.Size), HeaderSize: uint64(moov.Header)}
	var mvhd *mp4.Mvhd
	var tracks []*TrackBoxes
	var cur *TrackBoxes
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		bt := h.BoxInfo.Type.String()
		pathLen := len(h.Path)
		switch {
		case pathLen == 2 && bt == "mvhd":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			mvhd = box.(*mp4.Mvhd)
		case pathLen == 2 && bt == "trak":
			cur = &TrackBoxes{}
			tracks = append(tracks, cur)
		case pathLen == 4 && bt == "mdhd":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			mdhd := box.(*mp4.Mdhd)
			cur.Timescale = mdhd.Timescale
			cur.MDHDDur = mdhd.DurationV1
			if mdhd.GetVersion() == 0 {
				cur.MDHDDur = uint64(mdhd.DurationV0)
			}
		case pathLen == 4 && bt == "hdlr":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			cur.Handler = HandlerType(string(box.(*mp4.Hdlr).HandlerType[:]))
		case pathLen == 3 && bt == "tkhd":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			tkhd := box.(*mp4.Tkhd)
			cur.TrackID = tkhd.TrackID
			cur.Width = uint16(tkhd.Width >> 16)
			cur.Height = uint16(tkhd.Height >> 16)
			cur.Volume = uint16(tkhd.Volume)
		case pathLen == 6 && bt == "stsd":
			entry, err := readStsdEntry(f, &h.BoxInfo)
			if err != nil {
				return nil, err
			}
			cur.StsdEntry = entry
		case pathLen == 6 && stblBoxTypes[bt]:
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			switch v := box.(type) {
			case *mp4.Stts:
				cur.Stts = v
			case *mp4.Ctts:
				cur.Ctts = v
			case *mp4.Stsc:
				cur.Stsc = v
			case *mp4.Stsz:
				cur.Stsz = v
			case *mp4.Stco:
				cur.Stco = v
			case *mp4.Co64:
				cur.Co64 = v
			case *mp4.Stss:
				cur.Stss = v
			}
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructureFromInternal(section, root, handler); err != nil {
		if ce, ok := clock.AsClockError(err); ok {
			return nil, nil, ce
		}
		return nil, nil, clock.NewError(clock.KindBoxMalformed, "moov", err.Error())
	}
	return mvhd, tracks, nil
}

// ParseFile performs the scan-stage container parse. The returned FileInfo
// retains absolute byte offsets so later stages never re-parse headers.
func ParseFile(path string) (*FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	refs, moov, mdat, err := scanTopLevel(f)
	if err != nil {
		return nil, err
	}
	if moov.Size == 0 {
		return nil, clock.NewError(clock.KindBoxMalformed, "scan", "moov box missing")
	}
	if mdat.Size == 0 {
		return nil, clock.NewError(clock.KindBoxMalformed, "scan", "mdat box missing")
	}
	mvhd, tracks, err := parseMoov(f, moov)
	if err != nil {
		return nil, err
	}
	if len(tracks) == 0 {
		return nil, clock.NewError(clock.KindBoxMalformed, "moov", "no tracks found")
	}
	return &FileInfo{
		Path: path, Size: info.Size(), TopLevel: refs,
		Moov: moov, Mdat: mdat, Mvhd: mvhd, Tracks: tracks,
	}, nil
}
