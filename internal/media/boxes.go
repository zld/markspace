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

// parseMoov walks the whole file recursively with Expand and captures the
// sample tables needed for timeline construction and cutting. Box type is
// matched against its path so multiple tracks stay independent.
func parseMoov(f *os.File, moov BoxRef) (*mp4.Mvhd, []*TrackBoxes, error) {
	var mvhd *mp4.Mvhd
	var tracks []*TrackBoxes
	var cur *TrackBoxes
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		bt := h.BoxInfo.Type.String()
		path := h.Path
		switch {
		case bt == "mvhd":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			mvhd = box.(*mp4.Mvhd)
		case bt == "trak":
			cur = &TrackBoxes{}
			tracks = append(tracks, cur)
		case hasType(path, "tkhd"):
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			tkhd := box.(*mp4.Tkhd)
			cur.TrackID = tkhd.TrackID
			cur.Width = uint16(tkhd.Width >> 16)
			cur.Height = uint16(tkhd.Height >> 16)
			cur.Volume = uint16(tkhd.Volume)
		case hasType(path, "mdhd"):
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
		case hasType(path, "hdlr"):
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			cur.Handler = HandlerType(string(box.(*mp4.Hdlr).HandlerType[:]))
		case bt == "stsd":
			entry, err := readStsdEntry(f, &h.BoxInfo)
			if err != nil {
				return nil, err
			}
			cur.StsdEntry = entry
		case stblBoxTypes[bt]:
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
		// Sample descriptions (avc1/mp4a and their codec config children)
		// are captured raw via readStsdEntry; do not recurse into them.
		if bt == "stsd" || isSampleEntry(bt) {
			return nil, nil
		}
		return h.Expand()
	}
	if _, err := mp4.ReadBoxStructure(f, handler); err != nil {
		if ce, ok := clock.AsClockError(err); ok {
			return nil, nil, ce
		}
		return nil, nil, clock.NewError(clock.KindBoxMalformed, "moov", err.Error())
	}
	return mvhd, tracks, nil
}

// hasType reports whether a box path contains a box type.
func hasType(path mp4.BoxPath, want string) bool {
	for _, t := range path {
		if t.String() == want {
			return true
		}
	}
	return false
}

// isSampleEntry recognizes the codec sample-entry box types handled here.
func isSampleEntry(bt string) bool {
	switch bt {
	case "avc1", "avc3", "mp4v", "encv", "hev1", "hvc1",
		"mp4a", "enca", "avcC", "esds", "hvcC":
		return true
	}
	return false
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
