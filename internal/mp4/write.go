package mp4

import (
	"crypto/sha256"

	auditerrors "media-audit/internal/errors"
	"media-audit/internal/media"
	"media-audit/internal/timebase"
	"media-audit/internal/wire"
)

type TrackRequest struct {
	ID       uint32
	Timebase timebase.Timebase
	Width    uint32
	Height   uint32
	Metadata media.Metadata
	Samples  []media.SampleSpec
	Duration int64
}

func AppendBox(b *wire.Buffer, typ string, payload []byte) int {
	start := b.Len()
	b.U32(uint32(8 + len(payload)))
	b.Raw([]byte(typ))
	b.Raw(payload)
	return start
}

func ftypPayload() []byte {
	var b wire.Buffer
	b.Raw([]byte("isom"))
	b.U32(0x200)
	b.Raw([]byte("isom"))
	b.Raw([]byte("mp42"))
	return b.Bytes()
}

func matrix(rotation int32) []uint32 {
	identity := []uint32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
	switch rotation {
	case 90:
		return []uint32{0, 0x10000, 0, 0xffff0000, 0, 0, 0, 0, 0x40000000}
	case 180, -180:
		return []uint32{0xffff0000, 0, 0, 0, 0xffff0000, 0, 0, 0, 0x40000000}
	case 270, -90:
		return []uint32{0, 0xffff0000, 0, 0x10000, 0, 0, 0, 0, 0x40000000}
	default:
		return identity
	}
}

func tkhdPayload(req TrackRequest) []byte {
	var b wire.Buffer
	b.U32(3) // version 0, enabled + in-movie flags
	b.U32(0)
	b.U32(0)
	b.U32(req.ID)
	b.U32(0)
	b.U32(uint32(req.Duration))
	b.Raw(make([]byte, 8))
	b.U16(0)
	b.U16(0)
	b.U16(0)
	b.U16(0)
	for _, v := range matrix(req.Metadata.RotationDegrees) {
		b.U32(v)
	}
	b.U32(req.Width << 16)
	b.U32(req.Height << 16)
	return b.Bytes()
}

func mdhdPayload(ts uint32, duration int64) []byte {
	var b wire.Buffer
	b.U32(0)
	b.U32(0)
	b.U32(0)
	b.U32(ts)
	b.U32(uint32(duration))
	b.U16(0)
	b.U16(0)
	return b.Bytes()
}

func mvhdPayload(ts uint32, duration int64) []byte {
	var b wire.Buffer
	b.U32(0)
	b.U32(0)
	b.U32(0)
	b.U32(ts)
	b.U32(uint32(duration))
	b.U32(0x10000)
	b.U16(0x0100)
	b.Raw(make([]byte, 10))
	for _, v := range matrix(0) {
		b.U32(v)
	}
	b.Raw(make([]byte, 24))
	b.U32(2)
	return b.Bytes()
}

func clapPayload(c *media.Crop, w, h uint32) []byte {
	cw := w - uint32(c.Left+c.Right)
	ch := h - uint32(c.Top+c.Bottom)
	var b wire.Buffer
	b.U32(cw)
	b.U32(1)
	b.U32(ch)
	b.U32(1)
	b.U32(0)
	b.U32(1)
	b.U32(0)
	b.U32(1)
	return b.Bytes()
}

func colrPayload(c *media.Color) []byte {
	var b wire.Buffer
	b.Raw([]byte("nclx"))
	b.U16(c.Primaries)
	b.U16(c.Transfer)
	b.U16(c.Matrix)
	if c.FullRange {
		b.Byte(0x80)
	} else {
		b.Byte(0)
	}
	return b.Bytes()
}

// BuildTrackFile constructs a small but box-valid MP4 carrying the audit boxes.
func BuildTrackFile(req TrackRequest) []byte {
	ts := req.Timebase.Numerator
	if ts == 0 {
		ts = 90000
	}
	if req.Duration == 0 && len(req.Samples) > 0 {
		last := req.Samples[len(req.Samples)-1]
		req.Duration = last.PTS + int64(last.Duration)
	}

	var mdia wire.Buffer
	AppendBox(&mdia, "mdhd", mdhdPayload(ts, req.Duration))

	var trak wire.Buffer
	AppendBox(&trak, "tkhd", tkhdPayload(req))
	AppendBox(&trak, "mdia", mdia.Bytes())
	if req.Metadata.Crop != nil && req.Width > 0 && req.Height > 0 {
		AppendBox(&trak, "clap", clapPayload(req.Metadata.Crop, req.Width, req.Height))
	}
	if req.Metadata.Color != nil {
		AppendBox(&trak, "colr", colrPayload(req.Metadata.Color))
	}
	AppendBox(&trak, "amet", media.EncodeTrackMeta(req.ID, req.Timebase, req.Duration, req.Metadata))
	table := media.EncodeSampleTable(req.Samples)
	AppendBox(&trak, "asmp", table)
	AppendBox(&trak, "adig", media.EncodeIntegrity(0, 0, [sha256.Size]byte{}))

	var moov wire.Buffer
	AppendBox(&moov, "mvhd", mvhdPayload(ts, req.Duration))
	AppendBox(&moov, "trak", trak.Bytes())

	var file wire.Buffer
	AppendBox(&file, "ftyp", ftypPayload())
	AppendBox(&file, "moov", moov.Bytes())
	data := file.Bytes()

	var scanErrs []*auditerrors.Error
	boxes := scanBoxes(data, 0, int64(len(data)), &scanErrs)
	asmp := findBox(boxes, "asmp")
	adig := findBox(boxes, "adig")
	if asmp != nil && adig != nil {
		sum := sha256.Sum256(data[asmp.payloadStart:asmp.end])
		integ := media.EncodeIntegrity(asmp.payloadStart, asmp.end, sum)
		copy(data[adig.payloadStart:adig.end], integ)
	}
	return data
}
