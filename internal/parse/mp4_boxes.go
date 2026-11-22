package parse

import (
	mp4 "github.com/abema/go-mp4"
)

// cropBox carries the archived crop window (house subset, not a
// standard ISO box).
type cropBox struct {
	mp4.Box
	X uint32 `mp4:"0,size=32"`
	Y uint32 `mp4:"1,size=32"`
	W uint32 `mp4:"2,size=32"`
	H uint32 `mp4:"3,size=32"`
}

func (*cropBox) GetType() mp4.BoxType { return mp4.StrToBoxType("crop") }

// rtpBox carries one RTP synchronisation packet (12-byte header plus
// 8-byte payload) anchoring the track on the 90 kHz unified clock.
type rtpBox struct {
	mp4.Box
	Payload [20]byte `mp4:"0,size=8"`
}

func (*rtpBox) GetType() mp4.BoxType { return mp4.StrToBoxType("rtps") }

var mp4Containers = map[mp4.BoxType]bool{}

// mp4Leaf maps leaf box types to constructors; mp4Skippable lists
// infrastructure boxes that carry no data for this subset.
var (
	mp4Leaf      map[mp4.BoxType]func() mp4.IBox
	mp4Skippable map[mp4.BoxType]bool
)

func init() {
	mp4.AddBoxDef(&cropBox{})
	mp4.AddBoxDef(&rtpBox{})
	mp4Leaf = map[mp4.BoxType]func() mp4.IBox{
		mp4.BoxTypeTkhd():        func() mp4.IBox { return &mp4.Tkhd{} },
		mp4.BoxTypeMdhd():        func() mp4.IBox { return &mp4.Mdhd{} },
		mp4.BoxTypeElst():        func() mp4.IBox { return &mp4.Elst{} },
		mp4.BoxTypeStts():        func() mp4.IBox { return &mp4.Stts{} },
		mp4.BoxTypeStsz():        func() mp4.IBox { return &mp4.Stsz{} },
		mp4.BoxTypeStsc():        func() mp4.IBox { return &mp4.Stsc{} },
		mp4.BoxTypeStco():        func() mp4.IBox { return &mp4.Stco{} },
		mp4.BoxTypeStss():        func() mp4.IBox { return &mp4.Stss{} },
		mp4.BoxTypeColr():        func() mp4.IBox { return &mp4.Colr{} },
		mp4.StrToBoxType("crop"): func() mp4.IBox { return &cropBox{} },
		mp4.StrToBoxType("rtps"): func() mp4.IBox { return &rtpBox{} },
	}
	mp4Skippable = map[mp4.BoxType]bool{}
	for _, t := range []mp4.BoxType{
		mp4.BoxTypeFtyp(), mp4.BoxTypeMdat(), mp4.BoxTypeFree(),
		mp4.StrToBoxType("mvhd"), mp4.StrToBoxType("hdlr"), mp4.StrToBoxType("vmhd"),
		mp4.StrToBoxType("smhd"), mp4.StrToBoxType("dinf"), mp4.StrToBoxType("dref"),
		mp4.StrToBoxType("stsd"), mp4.StrToBoxType("stsh"), mp4.StrToBoxType("co64"),
	} {
		mp4Skippable[t] = true
	}
	for _, t := range []mp4.BoxType{
		mp4.StrToBoxType("moov"), mp4.StrToBoxType("trak"), mp4.StrToBoxType("mdia"),
		mp4.StrToBoxType("minf"), mp4.StrToBoxType("stbl"), mp4.StrToBoxType("edts"),
	} {
		mp4Containers[t] = true
	}
}
