package mp4ext

import (
	"github.com/abema/go-mp4"
)

// BoxTypeBcrc is the regression-profile per-sample CRC32 table box. It lives
// under stbl next to stco and lists one CRC32 per sample in decode order, so a
// stream consumer can verify each essence block without trusting its length.
func BoxTypeBcrc() mp4.BoxType { return mp4.StrToBoxType("bcrc") }

func init() {
	mp4.AddBoxDef(&Bcrc{})
}

// Bcrc is a full box carrying per-sample CRC32 values.
type Bcrc struct {
	mp4.FullBox `mp4:"0,extend"`
	EntryCount  uint32   `mp4:"1,size=32"`
	CRC32       []uint32 `mp4:"2,size=32,len=dynamic"`
}

// GetType implements mp4.IBox.
func (*Bcrc) GetType() mp4.BoxType { return BoxTypeBcrc() }

// GetFieldLength implements mp4.ICustomFieldObject.
func (b *Bcrc) GetFieldLength(name string, _ mp4.Context) uint {
	if name == "CRC32" {
		return uint(b.EntryCount)
	}
	panic("mp4ext: invalid dynamic field " + name)
}
