package probe

import (
	"encoding/binary"

	"tracecheck/internal/clock"
)

// RawSample is one indexed sample before integrity recovery.
type RawSample struct {
	Index    int // 0-based within the track
	Offset   clock.Offset
	Size     int64
	Delta    uint32 // duration in track timescale units
	Sync     bool
	Verified bool   // set during recovery once magic+CRC pass
}

// Track is one parsed representation on the shared 90 kHz clock.
type Track struct {
	ID        uint32
	Timescale uint32
	Handler   string
	Samples   []RawSample
	// MissingIndex names index boxes that were absent or unreadable, in order.
	MissingIndex []string
	Converter    clock.Converter
}

// ParseTracks walks a scanned tree and builds one Track per trak box. Missing
// sample-table boxes are recorded, not fatal here: recovery may rebuild the
// boundaries from frame magic instead. Structural length faults stay fatal.
func ParseTracks(data []byte, nodes []*Node) ([]*Track, []*clock.OffsetError) {
	var tracks []*Track
	var faults []*clock.OffsetError
	for _, trak := range FindAll(nodes, "trak") {
		tr, trFaults := parseTrack(data, trak)
		faults = append(faults, trFaults...)
		if tr != nil {
			tracks = append(tracks, tr)
		}
	}
	return tracks, faults
}

func parseTrack(data []byte, trak *Node) (*Track, []*clock.OffsetError) {
	var faults []*clock.OffsetError
	tr := &Track{ID: trackID(data, trak)}

	mdhd := findChildPath(trak, "mdia", "mdhd")
	if mdhd != nil {
		ts, tsErr := readMdhd(data, mdhd)
		if tsErr == nil {
			tr.Timescale = ts
		} else {
			faults = append(faults, tsErr)
		}
	}
	if hdlr := findChildPath(trak, "mdia", "hdlr"); hdlr != nil {
		tr.Handler = readHdlr(data, hdlr)
	}
	conv, err := clock.NewConverterForTrack(tr.ID, tr.Timescale)
	if err != nil {
		faults = append(faults, clock.NewOffsetError(trak.Offset,
			clock.CategoryMissingIndex, "no usable timescale", err))
		return nil, faults
	}
	tr.Converter = conv

	stbl := findChildPath(trak, "mdia", "minf", "stbl")
	if stbl == nil {
		faults = append(faults, clock.NewOffsetError(trak.Offset,
			clock.CategoryMissingIndex, "track has no sample table", nil))
		return tr, faults
	}

	deltas := readStts(data, stbl, &faults)
	sizes := readStsz(data, stbl, &faults)
	offsets := readOffsets(data, stbl, &faults, tr)
	syncs := readStss(data, stbl, &faults)

	n := maxLen(sizes, offsets, deltas)
	if n == 0 {
		tr.MissingIndex = append(tr.MissingIndex, "samples")
		return tr, faults
	}
	for i := 0; i < n; i++ {
		rs := RawSample{Index: i}
		if i < len(offsets) {
			rs.Offset = clock.Offset(offsets[i])
		} else {
			tr.MissingIndex = appendUnique(tr.MissingIndex, "stco")
		}
		if i < len(sizes) {
			rs.Size = int64(sizes[i])
		} else {
			tr.MissingIndex = appendUnique(tr.MissingIndex, "stsz")
		}
		if i < len(deltas) {
			rs.Delta = deltas[i]
		} else {
			tr.MissingIndex = appendUnique(tr.MissingIndex, "stts")
		}
		rs.Sync = len(syncs) == 0 || (i < len(syncs) && syncs[i])
		tr.Samples = append(tr.Samples, rs)
	}
	return tr, faults
}

func readMdhd(data []byte, n *Node) (uint32, *clock.OffsetError) {
	p, err := n.Payload(data)
	if err != nil {
		if oe, ok := err.(*clock.OffsetError); ok {
			return 0, oe
		}
		return 0, clock.NewOffsetError(n.PayloadOff, clock.CategoryBounds, "mdhd unreadable", err)
	}
	if len(p) < 4+20 {
		return 0, clock.NewOffsetError(n.PayloadOff, clock.CategoryTruncated,
			"mdhd payload too short", nil)
	}
	version := p[0]
	base := 4
	if version == 1 {
		base += 8 // creation/modification are 64-bit
	} else {
		base += 8
	}
	if len(p) < base+4 {
		return 0, clock.NewOffsetError(n.PayloadOff, clock.CategoryTruncated,
			"mdhd timescale out of range", nil)
	}
	return binary.BigEndian.Uint32(p[base : base+4]), nil
}

func readHdlr(data []byte, n *Node) string {
	p, err := n.Payload(data)
	if err != nil || len(p) < 12 {
		return ""
	}
	return string(p[8:12])
}

func trackID(data []byte, trak *Node) uint32 {
	tkhd := Find(trak.Children, "tkhd")
	if tkhd == nil {
		return 0
	}
	p, err := tkhd.Payload(data)
	if err != nil || len(p) < 4+8 {
		return 0
	}
	// full-box(4) + creation(4) + modification(4) + track_ID(4)
	return binary.BigEndian.Uint32(p[12:16])
}
