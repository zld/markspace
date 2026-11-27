package demux

import (
	mp4lib "github.com/abema/go-mp4"
)

// NclxColr is a fixed-length redefinition of the colr box. The built-in
// go-mp4 Colr marks ColourType as a C-string and the range/reserved pair as
// sub-byte bit fields; both leave the ReadBoxStructure cursor short when the
// fourCC has no NUL. Reading the final byte whole and splitting the top bit
// ourselves keeps the consumed length exactly 12 and every box aligned.
type NclxColr struct {
	mp4lib.Box
	ColourType              [4]byte `mp4:"0,size=8"`
	ColourPrimaries         uint16  `mp4:"1,size=16"`
	TransferCharacteristics uint16  `mp4:"2,size=16"`
	MatrixCoefficients      uint16  `mp4:"3,size=16"`
	RangeAndReserved        uint8   `mp4:"4,size=8"`
}

// FullRangeFlag is the high bit of the packed range/reserved byte.
func (c *NclxColr) FullRangeFlag() bool { return c.RangeAndReserved&0x80 != 0 }

// GetType returns the colr box type.
func (*NclxColr) GetType() mp4lib.BoxType { return mp4lib.BoxTypeColr() }

func init() {
	// Later registration wins in getBoxDef's reverse-order lookup.
	mp4lib.AddBoxDef(&NclxColr{})
}

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
