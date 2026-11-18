// Package timeline maps movie time to media time through the edit list
// and unwraps 32-bit decode-time wraparound.
package timeline

import (
	"encoding/binary"
	"fmt"
)

// Edit is one edit-list entry. MediaTime == -1 marks an empty edit
// (a hole in the media timeline).
type Edit struct {
	SegmentDuration int64   // in movie timescale units
	MediaTime       int64   // in media timescale units; -1 = empty edit
	Rate            float64 // playback rate; only 1.0 is supported
}

// ParseElst parses an elst box payload (full box: version/flags + entries).
// movieTS and mediaTS are the movie and media timescales, kept for callers
// that convert between the two domains; entries are stored unscaled.
func ParseElst(data []byte) ([]Edit, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("elst: truncated full-box header (%d bytes)", len(data))
	}
	version := data[0]
	count := int(binary.BigEndian.Uint32(data[4:8]))
	rest := data[8:]
	var step int
	switch version {
	case 0:
		step = 12
	case 1:
		step = 20
	default:
		return nil, fmt.Errorf("elst: unsupported version %d", version)
	}
	if count < 0 || len(rest) < count*step {
		return nil, fmt.Errorf("elst: entry count %d exceeds payload (%d bytes)", count, len(rest))
	}
	edits := make([]Edit, 0, count)
	for i := 0; i < count; i++ {
		e := rest[i*step:]
		var ed Edit
		if version == 1 {
			ed.SegmentDuration = int64(binary.BigEndian.Uint64(e[0:8]))
			ed.MediaTime = int64(binary.BigEndian.Uint64(e[8:16]))
			e = e[16:]
		} else {
			ed.SegmentDuration = int64(binary.BigEndian.Uint32(e[0:4]))
			ed.MediaTime = int64(int32(binary.BigEndian.Uint32(e[4:8])))
			e = e[8:]
		}
		rateInt := int16(binary.BigEndian.Uint16(e[0:2]))
		rateFrac := binary.BigEndian.Uint16(e[2:4])
		ed.Rate = float64(rateInt) + float64(rateFrac)/65536.0
		if ed.MediaTime < -1 {
			return nil, fmt.Errorf("elst: entry %d has negative media time %d", i, ed.MediaTime)
		}
		if ed.MediaTime != -1 && ed.Rate != 1.0 {
			return nil, fmt.Errorf("elst: entry %d has unsupported rate %.4f (only 1.0)", i, ed.Rate)
		}
		edits = append(edits, ed)
	}
	return edits, nil
}

// MapStatus describes how a movie time resolved against the edit list.
type MapStatus int

const (
	// MapOK: movie time maps to a media time.
	MapOK MapStatus = iota
	// MapHole: movie time falls inside an empty edit (timeline hole).
	MapHole
	// MapBeyondEnd: movie time is past the last edit segment.
	MapBeyondEnd
)

func (s MapStatus) String() string {
	switch s {
	case MapOK:
		return "ok"
	case MapHole:
		return "hole"
	case MapBeyondEnd:
		return "beyond-end"
	}
	return "unknown"
}

// MapMovieTime converts a movie-timeline position (movie timescale units)
// to a media-timeline position (media timescale units). Holes are
// preserved: they report MapHole instead of snapping to a neighbour.
func MapMovieTime(edits []Edit, movieTime, movieTS, mediaTS int64) (int64, MapStatus) {
	if movieTime < 0 {
		return 0, MapHole
	}
	var cursor int64
	for _, ed := range edits {
		if movieTime < cursor+ed.SegmentDuration {
			off := movieTime - cursor
			if ed.MediaTime == -1 {
				return 0, MapHole
			}
			// rate is guaranteed 1.0 by ParseElst.
			media := ed.MediaTime + off*mediaTS/movieTS
			return media, MapOK
		}
		cursor += ed.SegmentDuration
	}
	return 0, MapBeyondEnd
}
