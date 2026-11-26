package ebml

import (
	"pwflow/internal/fault"
)

func (p *parser) parseTracks(start, end int64) error {
	return eachChild(p.data, start, end, func(h Header, bodyEnd int64) error {
		if h.ID != IDTrackEntry {
			return nil
		}
		if len(p.rep.Tracks) >= p.limits.MaxTracks {
			return fault.New(fault.TooManyTracks, "more than %d tracks", p.limits.MaxTracks)
		}
		track := Track{}
		if err := p.parseTrackEntry(h.DataOffset, bodyEnd, &track); err != nil {
			return err
		}
		if track.Number == 0 {
			return fault.At(fault.EBMLBadBlock, h.DataOffset, "track entry missing TrackNumber")
		}
		p.rep.Tracks = append(p.rep.Tracks, track)
		return nil
	})
}

func (p *parser) parseTrackEntry(start, end int64, track *Track) error {
	return eachChild(p.data, start, end, func(h Header, bodyEnd int64) error {
		raw := p.data[h.DataOffset:bodyEnd]
		switch h.ID {
		case IDTrackNumber:
			track.Number = readUint(raw)
		case IDTrackType:
			track.Type = readUint(raw)
		case IDCodecID:
			track.Codec = readString(raw)
		case IDVideo:
			if err := eachChild(p.data, h.DataOffset, bodyEnd, func(vh Header, vend int64) error {
				vraw := p.data[vh.DataOffset:vend]
				switch vh.ID {
				case IDPixelWidth:
					track.Width = readUint(vraw)
				case IDPixelHeight:
					track.Height = readUint(vraw)
				}
				return nil
			}); err != nil {
				return fault.At(fault.EBMLHeader, h.DataOffset, "video track metadata: %w", err)
			}
		}
		return nil
	})
}
