package fixture

import (
	"encoding/binary"

	"mediaforensics/internal/model"
)

func buildMoov(spec MediaSpec, plans []trackPlan, layout *Layout) ([]byte, error) {
	traks := make([]*boxNode, 0, len(plans))
	for _, p := range plans {
		traks = append(traks, buildTrak(p, layout))
	}
	mvhd := leaf("mvhd", buildMvhd(spec))
	return container("moov", append([]*boxNode{mvhd}, traks...)...).serialize(), nil
}

func buildMvhd(spec MediaSpec) []byte {
	p := fullBoxPrefix(0, 0)
	p = append(p, u32(0)...)    // creation_time
	p = append(p, u32(0)...)    // modification_time
	p = append(p, u32(1000)...) // timescale
	p = append(p, u32(uint32(spec.Tracks[0].SampleCount)*1000)...)
	p = append(p, u32(0x00010000)...) // rate 1.0
	p = append(p, u16(0x0100)...)     // volume 1.0
	p = append(p, make([]byte, 10)...)
	p = append(p, identityMatrix()...)
	p = append(p, make([]byte, 24)...) // pre_defined
	p = append(p, u32(uint32(len(spec.Tracks))+1)...)
	return p
}

func identityMatrix() []byte {
	m := make([]byte, 36)
	for i := 0; i < 3; i++ {
		binary.BigEndian.PutUint32(m[i*12+i*4:], 0x00010000)
	}
	return m
}

func buildTrak(p trackPlan, layout *Layout) *boxNode {
	ts := p.spec
	tkhd := leaf("tkhd", buildTkhd(ts))
	mdhd := leaf("mdhd", buildMdhd(ts))
	hdlr := leaf("hdlr", buildHdlr(ts))
	vmhd := leaf("vmhd", append(fullBoxPrefix(0, 1), 0, 0, 0, 0, 0, 0, 0, 0))
	smhd := leaf("smhd", append(fullBoxPrefix(0, 0), 0, 0, 0, 0))
	dref := leaf("dref", buildDref())
	var mediaInfo *boxNode
	if ts.Kind == model.KindVideo {
		mediaInfo = container("minf", vmhd, container("dinf", dref), buildStbl(p, layout))
	} else {
		mediaInfo = container("minf", smhd, container("dinf", dref), buildStbl(p, layout))
	}
	mdia := container("mdia", mdhd, hdlr, mediaInfo)
	return container("trak", tkhd, mdia)
}

func buildTkhd(ts TrackSpec) []byte {
	p := fullBoxPrefix(0, 0x000007) // track_enabled|in_movie|in_preview
	p = append(p, u32(0)...)
	p = append(p, u32(0)...)
	p = append(p, u32(uint32(ts.ID))...)
	p = append(p, u32(0)...) // reserved
	dur := uint32(0)
	if ts.Timescale > 0 && ts.SampleCount > 0 {
		dur = uint32(int64(ts.SampleCount) * ts.Delta)
	}
	p = append(p, u32(dur)...)
	p = append(p, make([]byte, 8)...) // reserved
	p = append(p, 0, 0)               // layer
	p = append(p, 0, 0)               // alternate_group
	p = append(p, 0, 0)               // volume
	p = append(p, 0, 0)               // reserved
	p = append(p, identityMatrix()...)
	w := ts.Width
	h := ts.Height
	if w == 0 {
		w, h = 64, 48
	}
	p = append(p, u32(w<<16)...)
	p = append(p, u32(h<<16)...)
	return p
}

func buildMdhd(ts TrackSpec) []byte {
	p := fullBoxPrefix(0, 0)
	p = append(p, u32(0)...)
	p = append(p, u32(0)...)
	p = append(p, u32(uint32(ts.Timescale))...)
	dur := uint64(ts.SampleCount) * uint64(ts.Delta)
	p = append(p, u32(uint32(dur))...)
	p = append(p, 0x55, 0xC4) // language 'und'
	p = append(p, 0, 0)       // pre_defined
	return p
}

func buildHdlr(ts TrackSpec) []byte {
	p := fullBoxPrefix(0, 0)
	p = append(p, u32(0)...)
	ht := "soun"
	if ts.Kind == model.KindVideo {
		ht = "vide"
	}
	p = append(p, []byte(ht)...)
	p = append(p, make([]byte, 12)...)
	p = append(p, []byte("forensic-synthetic\x00")...)
	return p
}

func buildDref() []byte {
	p := fullBoxPrefix(0, 0)
	p = append(p, u32(1)...)
	url := appendBox(nil, "url ", fullBoxPrefix(0, 1))
	p = append(p, url...)
	return p
}
