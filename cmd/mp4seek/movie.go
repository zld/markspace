package main

import (
	"encoding/binary"
	"fmt"
	"os"

	"mp4seek/internal/bmff"
	"mp4seek/internal/timeline"
)

// trackInfo carries the per-track facts the commands need.
type trackInfo struct {
	ID        uint32
	Handler   string // "vide", "soun", ...
	Timescale uint32
	Duration  uint64
	Edits     []timeline.Edit
	Stbl      bmff.Box
	HasStbl   bool
	Path      string
}

// movie is the parsed moov plus fragment locations.
type movie struct {
	Timescale uint32
	Duration  uint64
	Tracks    []trackInfo
	Moov      bmff.Box
	Moofs     []bmff.Box
}

func (m *movie) track(id uint32) (*trackInfo, error) {
	for i := range m.Tracks {
		if m.Tracks[i].ID == id {
			return &m.Tracks[i], nil
		}
	}
	return nil, fmt.Errorf("%w: track %d not found", errUnsupported, id)
}

// openInput opens path and wraps it in a bmff.Reader.
func openInput(path string) (*bmff.Reader, *os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return bmff.NewReader(f, st.Size()), f, nil
}

// parseMvhdSelf decodes mvhd timescale/duration with our own parser so
// the go-mp4 decode can be cross-checked against it.
func parseMvhdSelf(data []byte) (timescale uint32, duration uint64, err error) {
	if len(data) < 20 {
		return 0, 0, fmt.Errorf("%w: mvhd too short", bmff.ErrCorrupt)
	}
	switch data[0] {
	case 0:
		timescale = binary.BigEndian.Uint32(data[12:16])
		duration = uint64(binary.BigEndian.Uint32(data[16:20]))
	case 1:
		if len(data) < 32 {
			return 0, 0, fmt.Errorf("%w: mvhd v1 too short", bmff.ErrCorrupt)
		}
		timescale = binary.BigEndian.Uint32(data[20:24])
		duration = binary.BigEndian.Uint64(data[24:32])
	default:
		return 0, 0, fmt.Errorf("%w: mvhd version %d", bmff.ErrCorrupt, data[0])
	}
	return timescale, duration, nil
}

// loadMovie walks the top level and parses moov into a movie.
func loadMovie(r *bmff.Reader) (*movie, error) {
	var moov *bmff.Box
	m := &movie{}
	err := r.WalkTop(func(b bmff.Box) (bool, error) {
		switch b.Type {
		case "moov":
			if moov != nil {
				return false, fmt.Errorf("%w: multiple moov boxes", bmff.ErrCorrupt)
			}
			bb := b
			moov = &bb
		case "moof":
			m.Moofs = append(m.Moofs, b)
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	if moov == nil {
		return nil, fmt.Errorf("%w: no moov box", bmff.ErrCorrupt)
	}
	m.Moov = *moov
	kids, err := r.Children(*moov)
	if err != nil {
		return nil, err
	}
	for _, k := range kids {
		switch k.Type {
		case "mvhd":
			data, err := r.ReadBoxData(k, bmff.MaxMetaBox)
			if err != nil {
				return nil, err
			}
			ts, dur, err := parseMvhdSelf(data)
			if err != nil {
				return nil, err
			}
			// Cross-check our parse against the go-mp4 decode.
			dec, err := r.DecodeMvhd(k)
			if err != nil {
				return nil, err
			}
			if err := bmff.CrossCheck("mvhd.timescale", uint64(ts), uint64(dec.Timescale)); err != nil {
				return nil, err
			}
			if err := bmff.CrossCheck("mvhd.duration", dur, dec.Duration); err != nil {
				return nil, err
			}
			m.Timescale, m.Duration = ts, dur
		case "trak":
			ti, err := loadTrack(r, k)
			if err != nil {
				return nil, err
			}
			m.Tracks = append(m.Tracks, ti)
		}
	}
	if m.Timescale == 0 {
		return nil, fmt.Errorf("%w: missing mvhd", bmff.ErrCorrupt)
	}
	return m, nil
}

// loadTrack parses one trak: tkhd via go-mp4, mdhd via go-mp4, hdlr and
// elst with our own parsers.
func loadTrack(r *bmff.Reader, trak bmff.Box) (trackInfo, error) {
	ti := trackInfo{Path: trak.Path}
	kids, err := r.Children(trak)
	if err != nil {
		return ti, err
	}
	for _, k := range kids {
		switch k.Type {
		case "tkhd":
			dec, err := r.DecodeTkhd(k)
			if err != nil {
				return ti, err
			}
			ti.ID = dec.TrackID
			ti.Duration = dec.Duration
		case "edts":
			ekids, err := r.Children(k)
			if err != nil {
				return ti, err
			}
			for _, ek := range ekids {
				if ek.Type != "elst" {
					continue
				}
				data, err := r.ReadBoxData(ek, bmff.MaxMetaBox)
				if err != nil {
					return ti, err
				}
				edits, err := timeline.ParseElst(data, ek.Path)
				if err != nil {
					return ti, err
				}
				ti.Edits = edits
			}
		case "mdia":
			if err := loadMdia(r, k, &ti); err != nil {
				return ti, err
			}
		}
	}
	if ti.ID == 0 {
		return ti, fmt.Errorf("%w: %s: missing tkhd", bmff.ErrCorrupt, trak.Path)
	}
	return ti, nil
}

func loadMdia(r *bmff.Reader, mdia bmff.Box, ti *trackInfo) error {
	kids, err := r.Children(mdia)
	if err != nil {
		return err
	}
	for _, k := range kids {
		switch k.Type {
		case "mdhd":
			dec, err := r.DecodeMdhd(k)
			if err != nil {
				return err
			}
			ti.Timescale = dec.Timescale
		case "hdlr":
			data, err := r.ReadBoxData(k, bmff.MaxMetaBox)
			if err != nil {
				return err
			}
			if len(data) < 12 {
				return fmt.Errorf("%w: %s: hdlr too short", bmff.ErrCorrupt, k.Path)
			}
			ti.Handler = string(data[8:12])
		case "minf":
			gkids, err := r.Children(k)
			if err != nil {
				return err
			}
			for _, g := range gkids {
				if g.Type == "stbl" {
					ti.Stbl = g
					ti.HasStbl = true
				}
			}
		}
	}
	return nil
}
