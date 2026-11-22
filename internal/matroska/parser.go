// Package matroska parses the subset of Matroska/WebM elements this kernel
// needs: tracks, clusters and media blocks. It streams over the bounded EBML
// reader, never decodes payloads, and fails closed on any structural defect.
package matroska

import (
	"io"

	"mediacheck/internal/ebml"
	"mediacheck/internal/timeline"
)

// Matroska element IDs used by the parser (subset).
const (
	IDEBMLHeader   uint32 = 0x1A45DFA3
	IDSegment      uint32 = 0x18538067
	IDSegmentInfo  uint32 = 0x1549A966
	IDTimecodeScale uint32 = 0x2AD7B1
	IDTracks       uint32 = 0x1654AE6B
	IDTrackEntry   uint32 = 0xAE
	IDTrackNumber  uint32 = 0xD7
	IDTrackType    uint32 = 0x83
	IDDefaultDur   uint32 = 0x23E383
	IDCluster      uint32 = 0x1F43B675
	IDTimecode     uint32 = 0xE7
	IDSimpleBlock  uint32 = 0xA3
	IDBlock        uint32 = 0xA1
)

// Block is one media block attributed to a track.
type Block struct {
	TrackNumber  uint64
	RelTimecode  int16 // signed, relative to cluster timecode
	Keyframe     bool
	AbsTimeTicks int64 // cluster timecode + relative timecode
	HeaderOffset int64 // first byte of the block element header
	PayloadSize  uint64
}

// TrackSpec is a declared track plus the clock built for it.
type TrackSpec struct {
	Number    uint64
	Type      uint8 // 1=video, 2=audio per Matroska
	DefaultDur uint64
	Clock     *timeline.Clock
}

// ParseError extends ebml.Error with the offending track number.
type ParseError struct {
	Class       ebml.ErrorClass
	Offset      int64
	TrackNumber uint64 // 0 when not block-specific
	Msg         string
}

func (e *ParseError) Error() string {
	return "matroska: " + string(e.Class) + " at offset=" + itoa(e.Offset) +
		" track=" + itoa(int64(e.TrackNumber)) + ": " + e.Msg
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func wrapErr(err error, track uint64) *ParseError {
	var ee *ebml.Error
	if asEBML(err, &ee) {
		return &ParseError{Class: ee.Class, Offset: ee.Offset, TrackNumber: track, Msg: ee.Msg}
	}
	return &ParseError{Class: ebml.ErrBadElement, Offset: -1, TrackNumber: track, Msg: err.Error()}
}

func asEBML(err error, target **ebml.Error) bool {
	for err != nil {
		if e, ok := err.(*ebml.Error); ok {
			*target = e
			return true
		}
		if u, ok := err.(interface{ Unwrap() error }); ok {
			err = u.Unwrap()
			continue
		}
		break
	}
	return false
}

// Parser streams one Matroska stream.
type Parser struct {
	r            *ebml.Reader
	lim          ebml.Limits
	tracks       map[uint64]*TrackSpec
	timecodeScale uint64 // nanoseconds per tick
}

// NewParser wraps a seekable EBML reader.
func NewParser(r *ebml.Reader, lim ebml.Limits) *Parser {
	return &Parser{r: r, lim: lim, tracks: map[uint64]*TrackSpec{}, timecodeScale: 1_000_000}
}

// Tracks returns the parsed track table.
func (p *Parser) Tracks() map[uint64]*TrackSpec { return p.tracks }

// TimecodeScale returns the nanoseconds-per-tick scale (default 1 ms).
func (p *Parser) TimecodeScale() uint64 { return p.timecodeScale }

// ParseHeader consumes EBML header, Segment header, SegmentInfo and Tracks.
// The returned segment element bounds cluster iteration; unknown-length
// segments get a hard end derived from the file size.
func (p *Parser) ParseHeader(fileSize int64) (*ebml.Element, error) {
	hdr, err := p.r.ReadElementHeader(0, -1)
	if err != nil {
		return nil, wrapErr(err, 0)
	}
	if hdr.ID != IDEBMLHeader {
		return nil, &ParseError{Class: ebml.ErrBadElement, Offset: hdr.HeaderOff, Msg: "first element is not EBML header"}
	}
	if err := p.r.SkipElement(hdr); err != nil {
		return nil, wrapErr(err, 0)
	}
	seg, err := p.r.ReadElementHeader(0, -1)
	if err != nil {
		return nil, wrapErr(err, 0)
	}
	if seg.ID != IDSegment {
		return nil, &ParseError{Class: ebml.ErrBadElement, Offset: seg.HeaderOff, Msg: "expected Segment element"}
	}
	hardEnd := fileSize
	if !seg.UnknownSize {
		hardEnd = seg.EndOff
	}
	if err := p.parseSegmentChildren(seg, hardEnd); err != nil {
		return nil, err
	}
	if len(p.tracks) == 0 {
		return nil, &ParseError{Class: ebml.ErrBadElement, Offset: seg.HeaderOff, Msg: "segment declares no tracks"}
	}
	return seg, nil
}

func (p *Parser) parseSegmentChildren(seg *ebml.Element, hardEnd int64) error {
	// Read pre-cluster metadata only. Bounded segments stop at the exact
	// EndOff; unknown-length segments stop at the first Cluster sibling.
	// Children() derives the correct boundary from the element itself.
	var it *ebml.ChildIterator
	if seg.UnknownSize {
		it = p.r.Children(seg, hardEnd, IDCluster)
	} else {
		it = p.r.Children(seg, hardEnd)
	}
	for {
		e, err := it.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return wrapErr(err, 0)
		}
		if e.ID == IDCluster {
			// Defensive: a bounded segment should reach EndOff first, but never
			// consume cluster bytes during header parsing.
			if err := p.r.SeekTo(e.HeaderOff); err != nil {
				return wrapErr(err, 0)
			}
			return nil
		}
		switch e.ID {
		case IDSegmentInfo:
			if err := p.parseInfo(e, hardEnd); err != nil {
				return err
			}
		case IDTracks:
			if err := p.parseTracks(e, hardEnd); err != nil {
				return err
			}
		default:
			if err := p.r.SkipElement(e); err != nil {
				return wrapErr(err, 0)
			}
		}
	}
}

func (p *Parser) parseInfo(info *ebml.Element, hardEnd int64) error {
	it := p.r.Children(info, hardEnd)
	for {
		e, err := it.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return wrapErr(err, 0)
		}
		if e.ID == IDTimecodeScale {
			v, err := p.r.ReadUint(int(e.Size))
			if err != nil {
				return wrapErr(err, 0)
			}
			p.timecodeScale = v
		} else if err := p.r.SkipElement(e); err != nil {
			return wrapErr(err, 0)
		}
	}
}

func (p *Parser) parseTracks(tracks *ebml.Element, hardEnd int64) error {
	it := p.r.Children(tracks, hardEnd)
	for {
		e, err := it.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return wrapErr(err, 0)
		}
		if e.ID != IDTrackEntry {
			if err := p.r.SkipElement(e); err != nil {
				return wrapErr(err, 0)
			}
			continue
		}
		if err := p.parseTrackEntry(e, hardEnd); err != nil {
			return err
		}
	}
}

func (p *Parser) parseTrackEntry(entry *ebml.Element, hardEnd int64) error {
	var number, trackType, defaultDur uint64
	it := p.r.Children(entry, hardEnd)
	for {
		e, err := it.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return wrapErr(err, 0)
		}
		switch e.ID {
		case IDTrackNumber, IDTrackType, IDDefaultDur:
			v, err := p.r.ReadUint(int(e.Size))
			if err != nil {
				return wrapErr(err, 0)
			}
			switch e.ID {
			case IDTrackNumber:
				number = v
			case IDTrackType:
				trackType = v
			case IDDefaultDur:
				defaultDur = v
			}
		default:
			if err := p.r.SkipElement(e); err != nil {
				return wrapErr(err, 0)
			}
		}
	}
	if number == 0 {
		return &ParseError{Class: ebml.ErrBadElement, Offset: entry.HeaderOff, Msg: "TrackEntry without TrackNumber"}
	}
	kind := timeline.KindAudio
	if trackType == 1 {
		kind = timeline.KindVideo
	}
	// Timecode ticks share the millisecond cluster scale; 32-bit wrap applies.
	cfg := timeline.TrackConfig{
		TrackNumber: number, Kind: kind,
		TicksPerSecond: 1_000_000_000 / maxu(p.timecodeScale, 1),
		WrapModulo:     1 << 32,
	}
	clock, err := timeline.NewClock(cfg)
	if err != nil {
		return &ParseError{Class: ebml.ErrBadElement, Offset: entry.HeaderOff, TrackNumber: number, Msg: err.Error()}
	}
	p.tracks[number] = &TrackSpec{Number: number, Type: uint8(trackType), DefaultDur: defaultDur, Clock: clock}
	return nil
}

func maxu(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
