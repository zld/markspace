// Package mp4ext registers the small set of ISO-BMFF boxes the regression
// profile needs but go-mp4 does not ship: the clean aperture box (clap).
//
// Registering is global in go-mp4, so importing this package is enough for
// both the fixture writer and the parser to see the definitions.
package mp4ext

import (
	"fmt"

	"github.com/abema/go-mp4"
)

// BoxTypeClap is the clean aperture box type.
func BoxTypeClap() mp4.BoxType { return mp4.StrToBoxType("clap") }

func init() {
	mp4.AddBoxDef(&Clap{})
}

// Clap is the ISO/IEC 14496-12 clean aperture box.
type Clap struct {
	mp4.Box
	CleanApertureWidthN  uint32 `mp4:"0,size=32"`
	CleanApertureWidthD  uint32 `mp4:"1,size=32"`
	CleanApertureHeightN uint32 `mp4:"2,size=32"`
	CleanApertureHeightD uint32 `mp4:"3,size=32"`
	HorizOffN            int32  `mp4:"4,size=32"`
	HorizOffD            uint32 `mp4:"5,size=32"`
	VertOffN             int32  `mp4:"6,size=32"`
	VertOffD             uint32 `mp4:"7,size=32"`
}

// GetType implements mp4.IBox.
func (*Clap) GetType() mp4.BoxType { return BoxTypeClap() }

// Width returns the aperture width when the denominator is valid.
func (c *Clap) Width() (uint32, error) {
	if c.CleanApertureWidthD == 0 {
		return 0, fmt.Errorf("mp4ext: clap width denominator is zero")
	}
	return c.CleanApertureWidthN / c.CleanApertureWidthD, nil
}

// Height returns the aperture height when the denominator is valid.
func (c *Clap) Height() (uint32, error) {
	if c.CleanApertureHeightD == 0 {
		return 0, fmt.Errorf("mp4ext: clap height denominator is zero")
	}
	return c.CleanApertureHeightN / c.CleanApertureHeightD, nil
}

// HorizOffset returns the horizontal offset when the denominator is valid.
func (c *Clap) HorizOffset() (int32, error) {
	if c.HorizOffD == 0 {
		return 0, fmt.Errorf("mp4ext: clap horizontal offset denominator is zero")
	}
	return c.HorizOffN / int32(c.HorizOffD), nil
}

// VertOffset returns the vertical offset when the denominator is valid.
func (c *Clap) VertOffset() (int32, error) {
	if c.VertOffD == 0 {
		return 0, fmt.Errorf("mp4ext: clap vertical offset denominator is zero")
	}
	return c.VertOffN / int32(c.VertOffD), nil
}
