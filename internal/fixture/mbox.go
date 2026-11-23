package fixture

import (
	"github.com/abema/go-mp4"
)

// MCID is a custom ISO BMFF box ("mediacheck id") registered with go-mp4 so
// that it is serialized through the real marshaller rather than hand-rolled.
// Init segments carry one MCID with track metadata; media segments carry one
// with the segment index/hint information.
type MCID struct {
	mp4.Box
	Version        uint8  `mp4:"0,size=8"`
	Flags          uint32 `mp4:"1,size=24"`
	Timescale      uint32 `mp4:"2,size=32"`
	TrackChannels  uint16 `mp4:"3,size=16"`
	SampleRate     uint32 `mp4:"4,size=32"`
	ColorPrimaries uint16 `mp4:"5,size=16"`
	TransferChar   uint16 `mp4:"6,size=16"`
	MatrixCoeffs   uint16 `mp4:"7,size=16"`
	SegmentSeq     uint32 `mp4:"8,size=32"`
	BaseDecodeTime uint64 `mp4:"9,size=64"`
	IndexPresent   uint8  `mp4:"10,size=8"`
}

// GetType returns the box type marker.
func (*MCID) GetType() mp4.BoxType { return mp4.StrToBoxType("mcid") }

func init() {
	mp4.AddBoxDef(&MCID{})
}

func brand(s string) [4]byte {
	var b [4]byte
	copy(b[:], s)
	return b
}
