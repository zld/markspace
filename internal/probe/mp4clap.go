package probe

import (
	"github.com/abema/go-mp4"

	"archive/internal/clock"
)

// mp4ClapBox is the clean-aperture box, which go-mp4 v1.2.0 does not model.
type mp4ClapBox struct {
	mp4.AnyTypeBox
	CleanApertureWidthN  int32 `mp4:"0,size=32"`
	CleanApertureWidthD  int32 `mp4:"1,size=32"`
	CleanApertureHeightN int32 `mp4:"2,size=32"`
	CleanApertureHeightD int32 `mp4:"3,size=32"`
	HorizOffN            int32 `mp4:"4,size=32"`
	HorizOffD            int32 `mp4:"5,size=32"`
	VertOffN             int32 `mp4:"6,size=32"`
	VertOffD             int32 `mp4:"7,size=32"`
}

func init() {
	mp4.AddAnyTypeBoxDef(&mp4ClapBox{}, mp4.StrToBoxType("clap"))
}

// cropFromClap converts clean aperture to an explicit crop rectangle inside
// the stored frame. Offsets are relative to the stored centre, matching the
// fixture generator and ISO/IEC 14496-12 semantics.
func cropFromClap(c *mp4ClapBox, storedW, storedH int) (CropDecl, *clock.Error) {
	if c.CleanApertureWidthD == 0 || c.CleanApertureHeightD == 0 ||
		c.HorizOffD == 0 || c.VertOffD == 0 {
		return CropDecl{}, clock.New(clock.KindInvalidMetadata, "clap has zero denominator")
	}
	w := int64(c.CleanApertureWidthN) / int64(c.CleanApertureWidthD)
	h := int64(c.CleanApertureHeightN) / int64(c.CleanApertureHeightD)
	hoff := int64(c.HorizOffN) / int64(c.HorizOffD)
	voff := int64(c.VertOffN) / int64(c.VertOffD)
	if w <= 0 || h <= 0 {
		return CropDecl{}, clock.Newf(clock.KindOutOfRange, "clap aperture %dx%d is not positive", w, h)
	}
	left := int64(storedW)/2 - w/2 + hoff
	top := int64(storedH)/2 - h/2 + voff
	return CropDecl{
		Left:   int(left),
		Right:  int(left + w),
		Top:    int(top),
		Bottom: int(top + h),
		Source: SourceClap,
	}, nil
}
