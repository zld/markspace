// Package index reads legacy keyframe indexes, validates them against the
// parsed bitstream and rebuilds invalid regions.
package index

import (
	"encoding/binary"
	"fmt"
	"io"

	"archive-recovery/internal/media"
)

// LegacyIndexMagic and version mark the on-disk legacy index format.
const (
	LegacyIndexMagic   = "ARCHIDX1"
	LegacyIndexVersion = uint16(1)
	headerSize         = 20 // 8 magic + 2 version + 4 reserved + 8 stream size
	entrySize          = 21 // 4 seq + 8 offset + 8 length + 1 keyframe
)

// Entry is one random-access index record.
type Entry struct {
	FrameSeq   uint32
	Offset     int64
	Length     int64
	IsKeyframe bool
}

// LegacyIndex is the parsed content of a legacy index file.
type LegacyIndex struct {
	Version    uint16
	StreamSize int64
	Entries    []Entry
}

// EntryStatus is the validation verdict for one legacy entry.
type EntryStatus struct {
	Entry  Entry
	Valid  bool
	Reason string
}

// ReadLegacyIndex parses the fixed binary layout:
//
//	[0:8]   magic "ARCHIDX1"
//	[8:10]  version uint16 LE
//	[10:12] reserved
//	[12:20] stream size uint64 LE (int64 range)
//	per entry (21 bytes LE):
//	       frame_seq uint32, offset uint64, length uint64, keyframe uint8
func ReadLegacyIndex(r io.Reader) (*LegacyIndex, error) {
	var hdr [headerSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, media.WrapError(media.ClassInvalidArgument, "index",
			"index header unreadable", err)
	}
	if string(hdr[0:8]) != LegacyIndexMagic {
		return nil, media.NewError(media.ClassInvalidArgument, "index", "bad index magic")
	}
	version := binary.LittleEndian.Uint16(hdr[8:10])
	if version != LegacyIndexVersion {
		return nil, media.NewError(media.ClassInvalidArgument, "index",
			fmt.Sprintf("unsupported legacy index version %d", version))
	}
	streamSize := int64(binary.LittleEndian.Uint64(hdr[12:20]))
	if streamSize < 0 {
		return nil, media.NewError(media.ClassInvalidArgument, "index", "stream size overflow")
	}

	var out []Entry
	var raw [entrySize]byte
	for {
		_, err := io.ReadFull(r, raw[:])
		if err == io.EOF {
			break
		}
		if err == io.ErrUnexpectedEOF {
			return nil, media.NewError(media.ClassInvalidArgument, "index",
				"trailing partial index entry")
		}
		if err != nil {
			return nil, media.WrapError(media.ClassInvalidArgument, "index", "entry read failed", err)
		}
		off := int64(binary.LittleEndian.Uint64(raw[4:12]))
		length := int64(binary.LittleEndian.Uint64(raw[12:20]))
		if off < 0 || length < 0 {
			return nil, media.NewError(media.ClassInvalidArgument, "index", "integer overflow in entry")
		}
		out = append(out, Entry{
			FrameSeq:   binary.LittleEndian.Uint32(raw[0:4]),
			Offset:     off,
			Length:     length,
			IsKeyframe: raw[20] == 1,
		})
	}
	return &LegacyIndex{Version: version, StreamSize: streamSize, Entries: out}, nil
}

// Validate checks every entry against actual frame units. An entry is valid
// only when its offset lands exactly on a frame boundary, its span matches
// the unit, frame sequence is the ordinal of that frame and the keyframe
// flag agrees with the parser.
func Validate(li *LegacyIndex, frames []media.FrameEvent, streamSize int64) []EntryStatus {
	byOffset := map[int64]media.FrameEvent{}
	for _, f := range frames {
		byOffset[f.Offset] = f
	}
	statuses := make([]EntryStatus, len(li.Entries))
	prevSeq := uint32(0)
	for i, e := range li.Entries {
		st := EntryStatus{Entry: e}
		switch {
		case e.Offset < 0 || e.Offset+e.Length > streamSize || e.Length <= 0:
			st.Reason = "offset_out_of_bounds"
		case e.FrameSeq != uint32(i):
			st.Reason = "frame_sequence_mismatch"
		case i > 0 && e.FrameSeq <= prevSeq:
			st.Reason = "frame_sequence_not_increasing"
		default:
			f, ok := byOffset[e.Offset]
			switch {
			case !ok || !f.IsFrame:
				st.Reason = "offset_not_a_frame_boundary"
			case f.Length != e.Length:
				st.Reason = "length_mismatch"
			case f.IsKeyframe != e.IsKeyframe:
				st.Reason = "keyframe_flag_mismatch"
			default:
				st.Valid = true
				st.Reason = "ok"
			}
		}
		statuses[i] = st
		prevSeq = e.FrameSeq
	}
	return statuses
}
