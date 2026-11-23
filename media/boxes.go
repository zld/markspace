package media

import (
	"github.com/abema/go-mp4"
)

// boxContainers lists the container boxes this kernel is willing to descend
// into. Anything else is treated as a leaf and validated against the parent's
// allow-list; unknown structural boxes are rejected rather than ignored.
var boxContainers = map[string]bool{
	"moov": true, "trak": true, "edts": true, "mdia": true,
	"minf": true, "dinf": true, "dref": true, "stbl": true,
}

var allowedChildren = map[string][]string{
	"ftyp": {},
	"moov": {"mvhd", "trak", "udta", "free", "skip", "meta"},
	"trak": {"tkhd", "edts", "mdia", "udta", "free", "skip", "tref"},
	"edts": {"elst"},
	"mdia": {"mdhd", "hdlr", "minf", "elng", "free", "skip"},
	"minf": {"vmhd", "smhd", "dinf", "stbl", "free", "skip", "hdlr"},
	"dinf": {"dref"},
	"dref": {"url ", "urn "},
	"stbl": {"stsd", "stts", "ctts", "stsc", "stsz", "stco", "co64", "stss", "sdtp", "free", "skip"},
	"mdat": {},
}

func allowedChild(parent, child string) bool {
	if allowedChildren[parent] == nil {
		return false
	}
	for _, c := range allowedChildren[parent] {
		if c == child {
			return true
		}
	}
	return false
}

// rawTrack holds the still-encoded boxes of a single trak.
type rawTrack struct {
	Trak Box
	Tkhd *mp4.Tkhd
	Mdhd *mp4.Mdhd
	Hdlr *mp4.Hdlr
	Elst *mp4.Elst
	Stsd Box
	Stts *mp4.Stts
	Ctts *mp4.Ctts
	Stsc *mp4.Stsc
	Stsz *mp4.Stsz
	Stco *mp4.Stco
	Co64 *mp4.Co64
	Stss *mp4.Stss
	// box header copies so sample-table errors can point at real offsets
	SttsBox, CttsBox, StscBox, StszBox, StcoBox, Co64Box, StssBox Box
	Codec                                                         string
	Entry                                                         string
}

// rawMovie is the parsed moov tree with uncompressed metadata only.
type rawMovie struct {
	Ftyp   *mp4.Ftyp
	Mvhd   *mp4.Mvhd
	Tracks []*rawTrack
	Mdat   []Box
}

func requireTrackBoxes(t *rawTrack) error {
	missing := ""
	switch {
	case t.Tkhd == nil:
		missing = "tkhd"
	case t.Mdhd == nil:
		missing = "mdhd"
	case t.Hdlr == nil:
		missing = "hdlr"
	case t.Stsd.Type == "":
		missing = "stsd"
	case t.Stts == nil:
		missing = "stts"
	case t.Stsc == nil:
		missing = "stsc"
	case t.Stsz == nil:
		missing = "stsz"
	case t.Stco == nil && t.Co64 == nil:
		missing = "stco/co64"
	}
	if missing != "" {
		return NewError(ClassUnsupported, "parse.trak", t.Trak.Offset, "required box %q missing", missing).WithTrack(t.TkhdTrackID())
	}
	return nil
}

// TkhdTrackID returns the track id or 0 when tkhd is absent.
func (t *rawTrack) TkhdTrackID() uint32 {
	if t.Tkhd == nil {
		return 0
	}
	return t.Tkhd.TrackID
}
