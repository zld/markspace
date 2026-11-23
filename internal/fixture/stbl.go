package fixture

import (
	"mediaforensics/internal/model"
)

func buildStbl(p trackPlan, layout *Layout) *boxNode {
	return buildStblWith(p, layout, p.spec.StblOption)
}

func buildStblWith(p trackPlan, layout *Layout, opt stblOption) *boxNode {
	ts := p.spec
	children := []*boxNode{
		leaf("stsd", buildStsd(ts)),
		leaf("stts", buildStts(ts)),
	}
	if !opt.omitStsc {
		children = append(children, leaf("stsc", buildStsc(p, opt.badStsc)))
	}
	if ts.Kind == model.KindVideo {
		children = append(children, leaf("stss", buildStss(ts)))
	}
	children = append(children, leaf("stsz", buildStsz(ts)))
	if !opt.omitStco {
		children = append(children, leaf("stco", buildStco(p, layout, opt.shiftStco)))
	}
	return container("stbl", children...)
}

func buildStsd(ts TrackSpec) []byte {
	p := fullBoxPrefix(0, 0)
	p = append(p, u32(1)...)
	var entry []byte
	if ts.Kind == model.KindVideo {
		entry = buildVisualEntry(ts)
	} else {
		entry = buildAudioEntry(ts)
	}
	p = append(p, appendBox(nil, entryType(ts), entry)...)
	return p
}

func entryType(ts TrackSpec) string {
	if ts.Kind == model.KindVideo {
		return "mfsV"
	}
	return "mfsA"
}

func buildVisualEntry(ts TrackSpec) []byte {
	p := make([]byte, 6)     // reserved
	p = append(p, u16(1)...) // data_reference_index
	p = append(p, u16(0)...) // pre_defined
	p = append(p, u16(0)...) // reserved
	p = append(p, make([]byte, 12)...)
	w := ts.Width
	h := ts.Height
	if w == 0 {
		w, h = 64, 48
	}
	p = append(p, u16(uint16(w))...)
	p = append(p, u16(uint16(h))...)
	p = append(p, u32(0x00480000)...) // horiz/vert resolution 72dpi
	p = append(p, u32(0x00480000)...)
	p = append(p, u32(0)...) // reserved
	p = append(p, u16(1)...) // frame_count
	p = append(p, make([]byte, 32)...)
	p = append(p, u16(0x0018)...) // depth
	p = append(p, u16(0xFFFF)...)
	return p
}

func buildAudioEntry(ts TrackSpec) []byte {
	p := make([]byte, 6)
	p = append(p, u16(1)...)
	p = append(p, u32(0)...)
	p = append(p, u32(0)...)
	p = append(p, u16(2)...)  // channels
	p = append(p, u16(16)...) // sample size
	p = append(p, u16(0)...)
	p = append(p, u16(0)...)
	p = append(p, u32(uint32(ts.Timescale))...)
	p = append(p, u32(0)...)
	return p
}

func buildStts(ts TrackSpec) []byte {
	p := fullBoxPrefix(0, 0)
	p = append(p, u32(1)...)
	p = append(p, u32(uint32(ts.SampleCount))...)
	p = append(p, u32(uint32(ts.Delta))...)
	return p
}

func buildStsc(p trackPlan, bad bool) []byte {
	perChunk := uint32(p.spec.FrameSamples)
	if p.spec.Kind == model.KindAudio {
		perChunk = 1
	}
	first := uint32(1)
	if bad {
		first = uint32(chunksFor(p.spec, p.spec.FrameSamples)) + 5
	}
	out := fullBoxPrefix(0, 0)
	out = append(out, u32(1)...)
	out = append(out, u32(first)...)
	out = append(out, u32(perChunk)...)
	out = append(out, u32(1)...) // sample_description_index
	return out
}

func buildStss(ts TrackSpec) []byte {
	// Every fourth sample is a keyframe.
	var keys []uint32
	for i := 0; i < ts.SampleCount; i += 4 {
		keys = append(keys, uint32(i+1))
	}
	p := fullBoxPrefix(0, 0)
	p = append(p, u32(uint32(len(keys)))...)
	for _, k := range keys {
		p = append(p, u32(k)...)
	}
	return p
}

func buildStsz(ts TrackSpec) []byte {
	p := fullBoxPrefix(0, 0)
	p = append(p, u32(0)...) // sample_size 0 => table of sizes
	p = append(p, u32(uint32(ts.SampleCount))...)
	for i := 0; i < ts.SampleCount; i++ {
		p = append(p, u32(uint32(ts.SampleSize))...)
	}
	return p
}

func buildStco(p trackPlan, layout *Layout, shift int64) []byte {
	// First moov pass only measures size; offsets are filled on pass two.
	if layout == nil {
		n := chunksFor(p.spec, p.spec.FrameSamples)
		out := fullBoxPrefix(0, 0)
		out = append(out, u32(uint32(n))...)
		out = append(out, make([]byte, 4*n)...)
		return out
	}
	tl := layout.Tracks[p.spec.ID]
	out := fullBoxPrefix(0, 0)
	out = append(out, u32(uint32(len(tl.ChunkOffset)))...)
	for _, off := range tl.ChunkOffset {
		v := uint32(off + shift)
		out = append(out, u32(v)...)
	}
	return out
}
