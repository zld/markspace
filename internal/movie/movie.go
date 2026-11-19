package movie

import (
	"fmt"

	"mp4seek/internal/box"
	"mp4seek/internal/timeline"
)

// SttsEntry is one time-to-sample run.
type SttsEntry struct {
	Count uint32
	Delta uint32
}

// CttsEntry is one composition-offset run (signed for version 1).
type CttsEntry struct {
	Count  uint32
	Offset int64
}

// StscEntry maps chunks to samples per chunk and a description index.
type StscEntry struct {
	FirstChunk      uint32
	SamplesPerChunk uint32
	DescIndex       uint32
}

// Track holds the parsed tables of one trak.
type Track struct {
	ID        int
	Handler   string
	Timescale int64
	Duration  int64 // media duration in media timescale units
	Edits     []timeline.Edit
	Descs     int // stsd entry count
	Stts      []SttsEntry
	Ctts      []CttsEntry
	Stss      []uint32 // 1-based sync sample numbers; nil = all sync
	HasStss   bool
	Stsc      []StscEntry
	Sizes     []int64 // per-sample sizes from stsz
	Chunks    []int64 // chunk offsets from stco/co64
}

// Movie is the parsed moov model.
type Movie struct {
	Timescale  int64
	Duration   int64
	Tracks     []*Track
	Fragmented bool // mvex/moof present: not supported
}

// find returns the first child box of the given type, or nil.
func find(rd *box.Reader, parent box.Box, typ string) (box.Box, error) {
	kids, err := rd.Children(parent)
	if err != nil {
		return box.Box{}, err
	}
	for _, k := range kids {
		if k.Type == typ {
			return k, nil
		}
	}
	return box.Box{}, nil
}

// Parse reads the top-level structure and the moov box into a Movie.
// Fragmented files (mvex/moof) are flagged, not parsed further.
func Parse(rd *box.Reader) (*Movie, error) {
	top, err := rd.TopLevel()
	if err != nil {
		return nil, err
	}
	m := &Movie{}
	var moov *box.Box
	for i := range top {
		switch top[i].Type {
		case "moov":
			b := top[i]
			moov = &b
		case "moof":
			m.Fragmented = true
		}
	}
	if moov == nil {
		return nil, fmt.Errorf("no moov box found (not a plain MP4)")
	}
	kids, err := rd.Children(*moov)
	if err != nil {
		return nil, err
	}
	for _, k := range kids {
		switch k.Type {
		case "mvex":
			m.Fragmented = true
		case "mvhd":
			if err := m.parseMvhd(rd, k); err != nil {
				return nil, err
			}
		}
	}
	if m.Timescale == 0 {
		return nil, fmt.Errorf("mvhd: missing or zero movie timescale")
	}
	return m, nil
}

func (m *Movie) parseMvhd(rd *box.Reader, b box.Box) error {
	data, err := rd.ReadData(b)
	if err != nil {
		return err
	}
	c := &cursor{b: data, typ: "mvhd"}
	v, err := c.u8()
	if err != nil {
		return err
	}
	if err := c.skip(3); err != nil { // flags
		return err
	}
	switch v {
	case 0:
		if err := c.skip(8); err != nil {
			return err
		}
		ts, err := c.u32()
		if err != nil {
			return err
		}
		dur, err := c.u32()
		if err != nil {
			return err
		}
		m.Timescale, m.Duration = int64(ts), int64(dur)
	case 1:
		if err := c.skip(16); err != nil {
			return err
		}
		ts, err := c.u32()
		if err != nil {
			return err
		}
		dur, err := c.u64()
		if err != nil {
			return err
		}
		m.Timescale, m.Duration = int64(ts), int64(dur)
	default:
		return fmt.Errorf("mvhd: unsupported version %d", v)
	}
	return nil
}
