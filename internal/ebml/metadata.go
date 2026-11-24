package ebml

import (
	"io"
)

// Matroska TrackEntry/Info element IDs.
const (
	IDInfoElement    uint32 = 0x1549A966
	IDTracksElement  uint32 = 0x1654AE6B
	IDTrackEntryEl   uint32 = 0xAE
	IDTrackNumberEl  uint32 = 0xD7
	IDTrackUIDEl     uint32 = 0x73C5
	IDTrackTypeEl    uint32 = 0x83
	IDCodecIDEl      uint32 = 0x86
	IDTimestampScale uint32 = 0x2AD7B1
)

// TrackEntryRaw is the verbatim track header information needed before any
// codec policy is applied.
type TrackEntryRaw struct {
	Number  uint64
	UID     uint64
	Type    uint64
	CodecID string
	// HeaderStart is the absolute offset of the TrackEntry element ID,
	// retained so codec-policy faults keep an input position.
	HeaderStart int64
}

// ReadMetadata walks the segment-level Info and Tracks masters. It returns
// the TimestampScale in nanoseconds-per-tick (Matroska default 1_000_000) and
// the track entries in file order. The segment reader is rewound afterwards
// so cluster scanning can start from the first segment child again.
func (s *Segment) ReadMetadata() (timeScaleNS uint64, tracks []TrackEntryRaw, err error) {
	timeScaleNS = 1_000_000 // Matroska spec default
	if err = s.r.Rewind(); err != nil {
		return 0, nil, err
	}
	for {
		el, rerr := s.r.ReadElement()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return 0, nil, rerr
		}
		switch el.ID {
		case IDInfoElement:
			if ts, e := readTimestampScale(s.r, el); e != nil {
				return 0, nil, e
			} else if ts > 0 {
				timeScaleNS = ts
			}
		case IDTracksElement:
			tracks, err = readTrackEntries(s.r, el)
			if err != nil {
				return 0, nil, err
			}
		default:
			if el.Size < 0 {
				dr, e := s.r.Child(el)
				if e != nil {
					return 0, nil, e
				}
				if e = dr.Drain(); e != nil {
					return 0, nil, e
				}
				continue
			}
			if err = s.r.Skip(el); err != nil {
				return 0, nil, err
			}
		}
	}
	if err = s.r.Rewind(); err != nil {
		return 0, nil, err
	}
	return timeScaleNS, tracks, nil
}

// Rewind restores the segment reader to the first segment payload byte.
func (r *Reader) Rewind() error { return r.SeekTo(r.base) }

func readTimestampScale(parent *Reader, info *Element) (uint64, error) {
	cr, err := parent.Child(info)
	if err != nil {
		return 0, err
	}
	for {
		el, err := cr.ReadElement()
		if err == io.EOF {
			return 1_000_000, nil
		}
		if err != nil {
			return 0, err
		}
		if el.ID == IDTimestampScale {
			buf, err := cr.ReadPayload(el)
			if err != nil {
				return 0, err
			}
			return ReadUint(buf)
		}
		if el.Size < 0 {
			dr, err := cr.Child(el)
			if err != nil {
				return 0, err
			}
			if err := dr.Drain(); err != nil {
				return 0, err
			}
			continue
		}
		if err := cr.Skip(el); err != nil {
			return 0, err
		}
	}
}

func readTrackEntries(parent *Reader, tracks *Element) ([]TrackEntryRaw, error) {
	cr, err := parent.Child(tracks)
	if err != nil {
		return nil, err
	}
	var out []TrackEntryRaw
	for {
		el, err := cr.ReadElement()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if el.ID != IDTrackEntryEl {
			if el.Size < 0 {
				dr, e := cr.Child(el)
				if e != nil {
					return nil, e
				}
				if e = dr.Drain(); e != nil {
					return nil, e
				}
				continue
			}
			if err := cr.Skip(el); err != nil {
				return nil, err
			}
			continue
		}
		entry, err := readOneTrackEntry(cr, el)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
}

func readOneTrackEntry(parent *Reader, te *Element) (TrackEntryRaw, error) {
	cr, err := parent.Child(te)
	if err != nil {
		return TrackEntryRaw{}, err
	}
	entry := TrackEntryRaw{HeaderStart: te.Start}
	for {
		el, err := cr.ReadElement()
		if err == io.EOF {
			return entry, nil
		}
		if err != nil {
			return entry, err
		}
		switch el.ID {
		case IDTrackNumberEl, IDTrackUIDEl, IDTrackTypeEl:
			buf, err := cr.ReadPayload(el)
			if err != nil {
				return entry, err
			}
			v, err := ReadUint(buf)
			if err != nil {
				return entry, err
			}
			switch el.ID {
			case IDTrackNumberEl:
				entry.Number = v
			case IDTrackUIDEl:
				entry.UID = v
			case IDTrackTypeEl:
				entry.Type = v
			}
		case IDCodecIDEl:
			buf, err := cr.ReadPayload(el)
			if err != nil {
				return entry, err
			}
			entry.CodecID = string(buf)
		default:
			if el.Size < 0 {
				dr, e := cr.Child(el)
				if e != nil {
					return entry, e
				}
				if e = dr.Drain(); e != nil {
					return entry, e
				}
				continue
			}
			if err := cr.Skip(el); err != nil {
				return entry, err
			}
		}
	}
}
