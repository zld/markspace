package demux

import (
	mp4lib "github.com/abema/go-mp4"
)

// Clap is the ISO 14496 clean-aperture box. go-mp4 v1.3.0 has no built-in
// definition, so we register one. All eight fields are 32-bit: width/height
// numerator-denominator pairs then horizontal/vertical offset pairs.
type Clap struct {
	mp4lib.Box
	CleanApertureWidthN  uint32 `mp4:"0,size=32"`
	CleanApertureWidthD  uint32 `mp4:"1,size=32"`
	CleanApertureHeightN uint32 `mp4:"2,size=32"`
	CleanApertureHeightD uint32 `mp4:"3,size=32"`
	HorizOffN            uint32 `mp4:"4,size=32"`
	HorizOffD            uint32 `mp4:"5,size=32"`
	VertOffN             uint32 `mp4:"6,size=32"`
	VertOffD             uint32 `mp4:"7,size=32"`
}

// GetType returns the clap box type.
func (*Clap) GetType() mp4lib.BoxType { return mp4lib.StrToBoxType("clap") }

func init() {
	mp4lib.AddBoxDef(&Clap{})
}
