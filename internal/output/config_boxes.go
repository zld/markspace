package output

import (
	"encoding/json"

	"medcheck/internal/adapter"
)

func audioSampleEntry(spec adapter.AudioSpec) []byte {
	p := make([]byte, 8+20)
	p[7] = 1 // data reference index
	ch := spec.Channels
	if ch == 0 {
		ch = 2
	}
	rate := spec.SampleRate
	if rate == 0 {
		rate = 48000
	}
	p[16] = byte(ch >> 8) // offset 8 + 8 reserved bytes
	p[17] = byte(ch)
	p[19] = 16 // sample size at 8+10/11
	binaryPutU32(p[24:28], uint32(rate)<<16)
	return box("mp4a", p)
}

func videoSampleEntry(spec adapter.VideoSpec) []byte {
	p := make([]byte, 8+70)
	p[7] = 1
	w, h := uint32(spec.Width), uint32(spec.Height)
	if w == 0 {
		w = 640
	}
	if h == 0 {
		h = 360
	}
	binaryPutU16(p[24:26], uint16(w))
	binaryPutU16(p[26:28], uint16(h))
	binaryPutU32(p[28:32], 0x00480000)
	binaryPutU32(p[32:36], 0x00480000)
	binaryPutU16(p[40:42], 1) // frame count
	binaryPutU16(p[74:76], 0x0018)
	binaryPutU16(p[76:78], 0xffff) // pre-defined = -1
	p = append(p, colrBox(spec.Color)...)
	return box("avc1", p)
}

func colrBox(c adapter.ColorMetadata) []byte {
	if !c.Present {
		c = adapter.DefaultColorPolicy().Default
	}
	p := []byte("nclx")
	p = append(p, byte(c.Primaries>>8), byte(c.Primaries))
	p = append(p, byte(c.Transfer>>8), byte(c.Transfer))
	p = append(p, byte(c.Matrix>>8), byte(c.Matrix))
	if c.FullRange {
		p = append(p, 0x80)
	} else {
		p = append(p, 0)
	}
	return box("colr", p)
}

func metadataBox(doc Document) ([]byte, error) {
	payload := struct {
		Generator string            `json:"generator"`
		Audio     adapter.AudioSpec `json:"audio"`
		Video     adapter.VideoSpec `json:"video"`
	}{Generator: "medcheck-v1", Audio: doc.Audio, Video: doc.Video}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return box("udta", box("medk", b)), nil
}

func binaryPutU16(b []byte, v uint16) {
	b[0] = byte(v >> 8)
	b[1] = byte(v)
}

func binaryPutU32(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}
