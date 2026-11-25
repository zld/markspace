package scan

import (
	"fmt"

	"github.com/abema/go-mp4"

	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/fault"
	"example.com/assetcheck/internal/index"
)

// ------------------------------------------------------------ container --

func (s *Scanner) scanContainer() {
	end := int64(len(s.data))
	for off := int64(0); off < end; {
		bi, err := s.boxAt(off)
		if err != nil {
			s.fail(fault.Wrap(fault.StageScan, "scan.box.header", off, clock.Invalid,
				"box header cannot be read", err))
			return
		}
		if bi.Size < bi.HeaderSize || int64(bi.Size) > end-off {
			s.fail(fault.New(fault.StageScan, "scan.box.truncated", off, clock.Invalid,
				fmt.Sprintf("%s box declares %d bytes but only %d remain", bi.Type, bi.Size, end-off)))
			return
		}
		switch bi.Type {
		case mp4.BoxTypeMoov():
			if !s.parseMoov(off, int64(bi.Size)) {
				return
			}
		case mp4.BoxTypeMoof():
			if !s.parseMoof(off, int64(bi.Size)) {
				return
			}
		case mp4.BoxTypeFtyp(), mp4.BoxTypeMdat(), mp4.BoxTypeFree(), mp4.BoxTypeStyp(),
			mp4.BoxTypeSidx():
			// Not needed for indexing; their extents are already validated.
		}
		off += int64(bi.Size)
	}
	if len(s.idx.Tracks) == 0 {
		s.fail(fault.New(fault.StageScan, "scan.moov.missing", fault.NoOffset, clock.Invalid,
			"input contains no moov with tracks"))
	}
}

// boxAt reads one box header at off without consuming the file position.

// parseMoov registers every track declared in moov.
func (s *Scanner) parseMoov(off, size int64) bool {
	payload := off + int64(headerSize(s.data, off))
	return s.walkChildren(payload, off+size, func(bi *mp4.BoxInfo) bool {
		if bi.Type != mp4.BoxTypeTrak() {
			return true
		}
		return s.parseTrak(bi)
	})
}

func (s *Scanner) parseTrak(trak *mp4.BoxInfo) bool {
	info := &TrackInfo{Codec: "unknown"}
	ok := s.walkChildren(int64(trak.Offset)+headerSize(s.data, int64(trak.Offset)), int64(trak.Offset+trak.Size),
		func(bi *mp4.BoxInfo) bool {
			switch bi.Type {
			case mp4.BoxTypeTkhd():
				var box mp4.Tkhd
				if err := s.decode(bi, &box); err != nil {
					s.fail(fault.Wrap(fault.StageScan, "scan.tkhd", int64(bi.Offset), clock.Invalid, "tkhd cannot be parsed", err))
					return false
				}
				info.ID = box.TrackID
			case mp4.BoxTypeMdia():
				return s.parseMdia(bi, info)
			}
			return true
		})
	if !ok {
		return false
	}
	if info.ID == 0 {
		s.fail(fault.New(fault.StageScan, "scan.trak.no_id", int64(trak.Offset), clock.Invalid, "track has no tkhd id"))
		return false
	}
	if info.Timescale == 0 {
		s.fail(fault.New(fault.StageScan, "scan.trak.zero_timescale", int64(trak.Offset), clock.Invalid,
			fmt.Sprintf("track %d has a zero timescale", info.ID)))
		return false
	}
	if _, exists := s.track[info.ID]; exists {
		s.fail(fault.New(fault.StageScan, "scan.trak.duplicate_id", int64(trak.Offset), clock.Invalid,
			fmt.Sprintf("track id %d is declared twice", info.ID)))
		return false
	}
	s.track[info.ID] = info
	s.order = append(s.order, info.ID)
	s.idx.Tracks = append(s.idx.Tracks, index.Track{
		ID:        info.ID,
		Kind:      info.Kind(),
		Handler:   string(info.Handler[:]),
		Codec:     info.Codec,
		Timescale: info.Timescale,
		Samples:   []index.Sample{},
	})
	return true
}

func (s *Scanner) parseMdia(mdia *mp4.BoxInfo, info *TrackInfo) bool {
	return s.walkChildren(int64(mdia.Offset)+headerSize(s.data, int64(mdia.Offset)), int64(mdia.Offset+mdia.Size),
		func(bi *mp4.BoxInfo) bool {
			switch bi.Type {
			case mp4.BoxTypeMdhd():
				var box mp4.Mdhd
				if err := s.decode(bi, &box); err != nil {
					s.fail(fault.Wrap(fault.StageScan, "scan.mdhd", int64(bi.Offset), clock.Invalid, "mdhd cannot be parsed", err))
					return false
				}
				info.Timescale = box.Timescale
			case mp4.BoxTypeHdlr():
				var box mp4.Hdlr
				if err := s.decode(bi, &box); err != nil {
					s.fail(fault.Wrap(fault.StageScan, "scan.hdlr", int64(bi.Offset), clock.Invalid, "hdlr cannot be parsed", err))
					return false
				}
				info.Handler = box.HandlerType
			case mp4.BoxTypeMinf():
				return s.walkChildren(int64(bi.Offset)+headerSize(s.data, int64(bi.Offset)), int64(bi.Offset+bi.Size),
					func(stbl *mp4.BoxInfo) bool {
						if stbl.Type != mp4.BoxTypeStbl() {
							return true
						}
						return s.walkChildren(int64(stbl.Offset)+headerSize(s.data, int64(stbl.Offset)), int64(stbl.Offset+stbl.Size),
							func(inner *mp4.BoxInfo) bool {
								if inner.Type == mp4.BoxTypeStsd() {
									info.Codec = s.stsdCodec(inner)
								}
								return true
							})
					})
			}
			return true
		})
}

// stsdCodec returns the four-character sample entry type mapped to a codec
// name, or "unknown".
func (s *Scanner) stsdCodec(stsd *mp4.BoxInfo) string {
	entryOff := int64(stsd.Offset) + int64(headerSize(s.data, int64(stsd.Offset))) + 8
	bi, err := s.boxAt(entryOff)
	if err != nil {
		return "unknown"
	}
	// Sample entries carry 8 bytes of SampleEntry fields; codec-private boxes
	// follow, but the entry type alone identifies the codec here.
	return codecName(bi.Type.String())
}

func codecName(entry string) string {
	switch entry {
	case "Opus":
		return "opus"
	case "mp4a":
		return "aac"
	case "sbtt", "stpp", "tx3g", "wvtt":
		return "subtitle"
	}
	return entry
}

// headerSize returns the encoded box header length at off (8 or 16).

// ------------------------------------------------------------- fragments --
