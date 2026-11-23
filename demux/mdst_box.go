package demux

import "github.com/abema/go-mp4"

// mdstBox carries fixture metadata (channel layout, sample rate, color
// provenance). go-mp4 has no definition for this private fourCC, so we
// register one ourselves at package init; registering an any-type box makes
// the parser accept it inside stbl without treating its payload as children.
func init() {
	mp4.AddAnyTypeBoxDef(&MdstBox{}, mp4.StrToBoxType(mdstBoxType))
}

// MdstBox is the registered representation of the private "mdst" box.
type MdstBox struct {
	mp4.AnyTypeBox
	Payload [8]byte `mp4:"0,size=8,sizeunit=byte"`
}
