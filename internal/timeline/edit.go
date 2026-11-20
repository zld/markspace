// Package timeline maps between media time and movie time using the
// track edit list (elst). Empty edits and rate-1 media edits are
// supported; any other edit rate is rejected with the box path.
package timeline

import (
	"encoding/binary"
	"errors"
	"fmt"
)

var ErrUnsupportedRate = errors.New("timeline: unsupported edit rate")

// Edit is one elst entry. MediaTime == -1 marks an empty edit.
type Edit struct {
	SegmentDuration uint64 // movie timescale ticks
	MediaTime       int64  // media timescale ticks, -1 for empty edit
	Rate            int32  // fixed-point 16.16; only 0x00010000 supported
}

// ParseElst decodes an elst box payload (version/flags included).
// path is the box path used in error messages.
func ParseElst(data []byte, path string) ([]Edit, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("timeline: %s: elst too short (%d bytes)", path, len(data))
	}
	version := data[0]
	count := binary.BigEndian.Uint32(data[4:8])
	if count > 1<<20 {
		return nil, fmt.Errorf("timeline: %s: elst entry count %d unreasonable", path, count)
	}
	entrySize := 12
	if version == 1 {
		entrySize = 20
	} else if version != 0 {
		return nil, fmt.Errorf("timeline: %s: elst version %d unsupported", path, version)
	}
	if uint64(count)*uint64(entrySize) > uint64(len(data)-8) {
		return nil, fmt.Errorf("timeline: %s: elst truncated: %d entries need %d bytes, have %d",
			path, count, uint64(count)*uint64(entrySize), len(data)-8)
	}
	edits := make([]Edit, 0, count)
	off := 8
	for i := uint32(0); i < count; i++ {
		var e Edit
		if version == 1 {
			e.SegmentDuration = binary.BigEndian.Uint64(data[off : off+8])
			e.MediaTime = int64(binary.BigEndian.Uint64(data[off+8 : off+16]))
			e.Rate = int32(binary.BigEndian.Uint32(data[off+16 : off+20]))
		} else {
			e.SegmentDuration = uint64(binary.BigEndian.Uint32(data[off : off+4]))
			e.MediaTime = int64(int32(binary.BigEndian.Uint32(data[off+4 : off+8])))
			e.Rate = int32(binary.BigEndian.Uint32(data[off+8 : off+12]))
		}
		off += entrySize
		if e.MediaTime != -1 && e.Rate != 0x00010000 {
			return nil, fmt.Errorf("%w: %s: entry %d rate 0x%08x", ErrUnsupportedRate, path, i, uint32(e.Rate))
		}
		edits = append(edits, e)
	}
	return edits, nil
}

// Mapper converts between media and movie time for one track.
// With no edits it is the identity map.
type Mapper struct {
	edits []Edit
}

// NewMapper builds a Mapper from parsed edits (nil for identity).
func NewMapper(edits []Edit) *Mapper { return &Mapper{edits: edits} }

// Empty reports whether any edit is present.
func (m *Mapper) Empty() bool { return len(m.edits) == 0 }

// MediaToMovie maps a media presentation time to movie ticks.
// ok is false when the media time is not presented by any edit.
func (m *Mapper) MediaToMovie(mediaPTS int64) (movie int64, ok bool) {
	if len(m.edits) == 0 {
		return mediaPTS, true
	}
	var acc int64
	for _, e := range m.edits {
		seg := int64(e.SegmentDuration)
		if e.MediaTime == -1 {
			acc += seg
			continue
		}
		if mediaPTS >= e.MediaTime && mediaPTS < e.MediaTime+seg {
			return acc + (mediaPTS - e.MediaTime), true
		}
		acc += seg
	}
	return 0, false
}

// MovieToMedia maps movie ticks to a media presentation time.
// ok is false when the movie time falls in an empty edit or past the end.
func (m *Mapper) MovieToMedia(movie int64) (media int64, ok bool) {
	if len(m.edits) == 0 {
		return movie, true
	}
	var acc int64
	for _, e := range m.edits {
		seg := int64(e.SegmentDuration)
		if movie < acc+seg {
			if e.MediaTime == -1 {
				return 0, false
			}
			return e.MediaTime + (movie - acc), true
		}
		acc += seg
	}
	return 0, false
}

// MovieDuration returns the total presented movie duration in ticks.
func (m *Mapper) MovieDuration() int64 {
	if len(m.edits) == 0 {
		return -1 // unknown
	}
	var acc int64
	for _, e := range m.edits {
		acc += int64(e.SegmentDuration)
	}
	return acc
}
