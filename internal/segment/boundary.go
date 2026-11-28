// Package segment cuts unit streams into keyframe-aligned segments on
// the shared integer tick line. Out-of-range slices are rejected with
// the offending offset.
package segment

import (
	"crypto/sha256"
	"encoding/hex"

	"mediacheck/internal/clock"
	"mediacheck/internal/codec"
	"mediacheck/internal/fault"
)

// Segment is one exported slice of a unit stream.
type Segment struct {
	Index     int
	StartTick clock.Ticks
	EndTick   clock.Ticks
	Units     []codec.Unit
	Bytes     int
	SHA256    string
}

// Cut splits units into segments of at least segTicks each. A cut is
// made only at a keyframe unit; ticks[i] is the presentation tick of
// units[i] on the shared line. data is the full source stream, used
// to hash the exact exported byte ranges.
func Cut(units []codec.Unit, ticks []clock.Ticks, segTicks clock.Ticks, data []byte) ([]Segment, error) {
	switch {
	case len(units) == 0:
		return nil, fault.New(fault.ErrSliceOutOfRange, 0, "cannot segment an empty stream")
	case len(units) != len(ticks):
		return nil, fault.New(fault.ErrSliceOutOfRange, int64(len(ticks)),
			"%d ticks for %d units", len(ticks), len(units))
	case segTicks <= 0:
		return nil, fault.New(fault.ErrSliceOutOfRange, 0, "segment length must be positive ticks")
	case !units[0].Keyframe:
		return nil, fault.New(fault.ErrSliceOutOfRange, units[0].Offset,
			"stream does not start at a keyframe")
	}
	var segs []Segment
	start := 0
	for i := 1; i <= len(units); i++ {
		atEnd := i == len(units)
		if !atEnd && !(units[i].Keyframe && ticks[i]-ticks[start] >= segTicks) {
			continue
		}
		seg, err := makeSegment(len(segs), units[start:i], ticks[start:i], data)
		if err != nil {
			return nil, err
		}
		segs = append(segs, seg)
		start = i
	}
	return segs, nil
}

func makeSegment(idx int, units []codec.Unit, ticks []clock.Ticks, data []byte) (Segment, error) {
	first, last := units[0], units[len(units)-1]
	lo := first.StartCode
	hi := last.Offset + int64(last.Size)
	if lo < 0 || hi > int64(len(data)) || lo >= hi {
		return Segment{}, fault.New(fault.ErrSliceOutOfRange, lo,
			"segment byte range [%d,%d) outside stream of %d bytes", lo, hi, len(data))
	}
	sum := sha256.Sum256(data[lo:hi])
	return Segment{
		Index:     idx,
		StartTick: ticks[0],
		EndTick:   ticks[len(ticks)-1],
		Units:     units,
		Bytes:     int(hi - lo),
		SHA256:    hex.EncodeToString(sum[:]),
	}, nil
}
