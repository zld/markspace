package mp4box

import (
	"sort"
	"strings"
)

// Sample is one sample to mux, in decode order.
type Sample struct {
	Data     []byte
	DTS      uint64
	Duration uint32
	Sync     bool // used only for video tracks
}

// Track describes a track written into moov and its mdat payload.
type Track struct {
	ID         uint32
	Handler    string // "vide" or "soun"
	Timescale  uint32
	Width      uint16
	Height     uint16
	Volume     int16
	SampleRate uint32
	Channel    uint16
	Codec      string // fourcc of the sample entry
	AvcC       []byte // when non-nil, emitted as an avcC child
	Samples    []Sample
}

// chunkSamples is the maximum number of samples placed in one chunk.
const chunkSamples = 16

// BuildOpts controls ftyp branding and free padding so wrappers can force an
// envelope-only change without touching sample bytes.
type BuildOpts struct {
	MajorBrand       string
	CompatibleBrands []string
	FreePadding      int
}

// DefaultOpts returns the standard CMAF brand set.
func DefaultOpts() BuildOpts {
	return BuildOpts{MajorBrand: "isom", CompatibleBrands: []string{"isom", "iso6", "mp41", "cmfc"}}
}

// Build produces a complete progressive MP4 file. The mdat is streamed as a
// single top-level box preceded by ftyp and moov; chunk offsets are computed
// after laying out ftyp+moov.
func Build(tracks []Track, opts BuildOpts) []byte {
	ftyp := buildFtyp(opts)
	var free []byte
	if opts.FreePadding > 0 {
		free = Box("free", Zeros(opts.FreePadding))
	}
	// First pass with zero chunk offsets to measure moov size.
	moov := buildMoov(tracks, nil)
	head := len(ftyp) + len(free) + len(moov)
	offsets := layoutOffsets(tracks, head+8)
	moov = buildMoov(tracks, offsets)

	out := make([]byte, 0, head+len(mdatPayload(tracks))+8)
	out = append(out, ftyp...)
	out = append(out, free...)
	out = append(out, moov...)
	mdat := mdatPayload(tracks)
	out = append(out, Box("mdat", mdat)...)
	return out
}

// layoutOffsets computes per-track chunk offsets for the first chunk of each
// chunk group, walking tracks in file order.
func layoutOffsets(tracks []Track, base int) [][]uint64 {
	offsets := make([][]uint64, len(tracks))
	cursor := uint64(base)
	for ti, tr := range tracks {
		for i := 0; i < len(tr.Samples); i += chunkSamples {
			offsets[ti] = append(offsets[ti], cursor)
			for j := i; j < len(tr.Samples) && j < i+chunkSamples; j++ {
				cursor += uint64(len(tr.Samples[j].Data))
			}
		}
	}
	return offsets
}

func mdatPayload(tracks []Track) []byte {
	var out []byte
	for _, tr := range tracks {
		for _, s := range tr.Samples {
			out = append(out, s.Data...)
		}
	}
	return out
}

func buildFtyp(opts BuildOpts) []byte {
	major := opts.MajorBrand
	if major == "" {
		major = "isom"
	}
	payload := make([]byte, 0, 16)
	payload = append(payload, padded4(major)...)
	payload = append(payload, U32(0x200)...)
	brands := opts.CompatibleBrands
	if len(brands) == 0 {
		brands = []string{"isom"}
	}
	for _, b := range brands {
		payload = append(payload, padded4(b)...)
	}
	return Box("ftyp", payload)
}

func padded4(s string) []byte {
	b := make([]byte, 4)
	copy(b, s)
	for i := len(s); i < 4; i++ {
		b[i] = ' '
	}
	return b
}

func buildMoov(tracks []Track, chunkOffsets [][]uint64) []byte {
	mvhd := buildMvhd(tracks)
	kids := [][]byte{mvhd}
	for i, tr := range tracks {
		var offs []uint64
		if chunkOffsets != nil {
			offs = chunkOffsets[i]
		}
		kids = append(kids, buildTrak(tr, offs))
	}
	return Container("moov", kids...)
}

func movieDuration(tracks []Track) uint64 {
	var max uint64
	for _, tr := range tracks {
		var d uint64
		for _, s := range tr.Samples {
			d += uint64(s.Duration)
		}
		if d > max {
			max = d
		}
	}
	return max
}

func buildMvhd(tracks []Track) []byte {
	p := []byte{}
	p = append(p, U32(0)...) // creation
	p = append(p, U32(0)...) // modification
	p = append(p, U32(1000)...)
	p = append(p, U32(uint32(movieDuration(tracks)))...)
	p = append(p, U32(0x00010000)...) // rate 1.0
	p = append(p, I16(0x0100)...)     // volume 1.0
	p = append(p, Zeros(2)...)
	p = append(p, Zeros(8)...)
	p = append(p, Matrix()...)
	p = append(p, Zeros(24)...)
	p = append(p, U32(uint32(len(tracks)+1))...)
	return FullBox("mvhd", 0, 0, p)
}

func buildTrak(tr Track, chunkOffsets []uint64) []byte {
	return Container("trak",
		buildTkhd(tr),
		Container("mdia",
			buildMdhd(tr),
			buildHdlr(tr),
			Container("minf",
				buildMediaHeader(tr),
				buildDinf(),
				buildStbl(tr, chunkOffsets),
			),
		),
	)
}

func trackDuration(tr Track) uint64 {
	var d uint64
	for _, s := range tr.Samples {
		d += uint64(s.Duration)
	}
	return d
}

func buildTkhd(tr Track) []byte {
	p := []byte{}
	p = append(p, U32(0)...)
	p = append(p, U32(0)...)
	p = append(p, U32(tr.ID)...)
	p = append(p, U32(0)...)
	p = append(p, U32(uint32(trackDuration(tr)))...)
	p = append(p, Zeros(8)...)
	p = append(p, I16(0)...)         // layer
	p = append(p, I16(0)...)         // alternate group
	p = append(p, I16(tr.Volume)...) // media volume
	p = append(p, Zeros(2)...)
	p = append(p, Matrix()...)
	p = append(p, Fixed1616(uint32(tr.Width))...)
	p = append(p, Fixed1616(uint32(tr.Height))...)
	flags := uint32(0x000003) // enabled | in movie
	if tr.Handler == "soun" {
		flags = 0x000003
	}
	return FullBox("tkhd", 0, flags, p)
}

func buildMdhd(tr Track) []byte {
	p := []byte{}
	p = append(p, U32(0)...)
	p = append(p, U32(0)...)
	p = append(p, U32(tr.Timescale)...)
	p = append(p, U32(uint32(trackDuration(tr)))...)
	p = append(p, language("und")...)
	p = append(p, U16(0)...)
	return FullBox("mdhd", 0, 0, p)
}

func language(code string) []byte {
	// ISO-639-2/T packed as three 5-bit values.
	for len(code) < 3 {
		code += " "
	}
	var bits uint16
	for i := 0; i < 3; i++ {
		bits = bits<<5 | uint16(code[i]-0x60)
	}
	return U16(bits)
}

func buildHdlr(tr Track) []byte {
	p := []byte{}
	p = append(p, U32(0)...)
	p = append(p, padded4(tr.Handler)...)
	p = append(p, Zeros(12)...)
	name := "VideoHandler"
	if tr.Handler == "soun" {
		name = "SoundHandler"
	} else if tr.Handler == "text" {
		name = "TextHandler"
	}
	p = append(p, []byte(name)...)
	p = append(p, 0)
	return FullBox("hdlr", 0, 0, p)
}

func buildMediaHeader(tr Track) []byte {
	if tr.Handler == "soun" {
		return FullBox("smhd", 0, 0, I16(0), U16(0))
	}
	return FullBox("vmhd", 0, 1, U16(0), U16(0), U16(0), U16(0))
}

func buildDinf() []byte {
	url := FullBox("url ", 0, 1)
	dref := FullBox("dref", 0, 0, U32(1), url)
	return Container("dinf", dref)
}

func buildStsd(tr Track) []byte {
	entry := buildSampleEntry(tr)
	// EntryCount then sample entry(ies).
	return FullBox("stsd", 0, 0, U32(1), entry)
}

func buildSampleEntry(tr Track) []byte {
	if tr.Handler == "soun" {
		p := []byte{}
		p = append(p, Zeros(6)...)
		p = append(p, U16(1)...) // data reference index
		p = append(p, Zeros(8)...)
		p = append(p, U16(tr.Channel)...)
		p = append(p, U16(16)...)
		p = append(p, U16(0)...)
		p = append(p, U16(0)...)
		p = append(p, U32(tr.SampleRate<<16)...)
		return Box(tr.Codec, p)
	}
	p := []byte{}
	p = append(p, Zeros(6)...)
	p = append(p, U16(1)...)
	p = append(p, Zeros(16)...)
	p = append(p, U16(tr.Width)...)
	p = append(p, U16(tr.Height)...)
	p = append(p, U32(0x00480000)...)
	p = append(p, U32(0x00480000)...)
	p = append(p, U32(0)...)
	p = append(p, U16(1)...)    // frame count
	p = append(p, Zeros(32)...) // compressor name
	p = append(p, U16(0x0018)...)
	p = append(p, I16(-1)...)
	kids := [][]byte{p}
	if len(tr.AvcC) > 0 {
		kids = append(kids, Box("avcC", tr.AvcC))
	}
	return Box(tr.Codec, kids...)
}

func buildStbl(tr Track, chunkOffsets []uint64) []byte {
	kids := [][]byte{buildStsd(tr)}
	kids = append(kids, buildStts(tr))
	if tr.Handler == "vide" {
		if stss := buildStss(tr); stss != nil {
			kids = append(kids, stss)
		}
	}
	kids = append(kids, buildStsc(tr))
	kids = append(kids, buildStsz(tr))
	kids = append(kids, buildStco(tr, chunkOffsets))
	return Container("stbl", kids...)
}

func buildStts(tr Track) []byte {
	type run struct {
		count uint32
		delta uint32
	}
	var runs []run
	for _, s := range tr.Samples {
		if n := len(runs); n > 0 && runs[n-1].delta == s.Duration {
			runs[n-1].count++
			continue
		}
		runs = append(runs, run{count: 1, delta: s.Duration})
	}
	p := append([]byte{}, U32(uint32(len(runs)))...)
	for _, r := range runs {
		p = append(p, U32(r.count)...)
		p = append(p, U32(r.delta)...)
	}
	return FullBox("stts", 0, 0, p)
}

func buildStss(tr Track) []byte {
	var nums []uint32
	for i, s := range tr.Samples {
		if s.Sync {
			nums = append(nums, uint32(i+1))
		}
	}
	if len(nums) == len(tr.Samples) && len(nums) > 0 {
		return nil // all samples are sync: omit stss
	}
	if len(nums) == 0 {
		return FullBox("stss", 0, 0, U32(0))
	}
	p := append([]byte{}, U32(uint32(len(nums)))...)
	for _, n := range nums {
		p = append(p, U32(n)...)
	}
	return FullBox("stss", 0, 0, p)
}

func buildStsc(tr Track) []byte {
	// Chunks are fixed-size groups of chunkSamples except possibly the last.
	total := len(tr.Samples)
	if total == 0 {
		return FullBox("stsc", 0, 0, U32(0))
	}
	type entry struct {
		first uint32
		per   uint32
		num   uint32
	}
	var entries []entry
	chunk := uint32(1)
	for i := 0; i < total; i += chunkSamples {
		n := uint32(chunkSamples)
		if total-i < chunkSamples {
			n = uint32(total - i)
		}
		if len(entries) == 0 || entries[len(entries)-1].per != n {
			entries = append(entries, entry{first: chunk, per: n, num: 1})
		}
		chunk++
	}
	p := append([]byte{}, U32(uint32(len(entries)))...)
	for _, e := range entries {
		p = append(p, U32(e.first)...)
		p = append(p, U32(e.per)...)
		p = append(p, U32(1)...)
	}
	return FullBox("stsc", 0, 0, p)
}

func buildStsz(tr Track) []byte {
	sizes := make([]uint32, len(tr.Samples))
	uniform := true
	for i, s := range tr.Samples {
		sizes[i] = uint32(len(s.Data))
		if i > 0 && sizes[i] != sizes[0] {
			uniform = false
		}
	}
	var sampleSize uint32
	if uniform && len(sizes) > 0 {
		sampleSize = sizes[0]
	}
	p := []byte{}
	p = append(p, U32(sampleSize)...)
	p = append(p, U32(uint32(len(sizes)))...)
	if sampleSize == 0 {
		for _, s := range sizes {
			p = append(p, U32(s)...)
		}
	}
	return FullBox("stsz", 0, 0, p)
}

func buildStco(tr Track, chunkOffsets []uint64) []byte {
	p := append([]byte{}, U32(uint32(len(chunkOffsets)))...)
	for _, o := range chunkOffsets {
		p = append(p, U32(uint32(o))...)
	}
	return FullBox("stco", 0, 0, p)
}

// KeyframeTimes returns the DTS of every sync sample in the track, used by
// fixture builders to request deterministic keyframe placement.
func KeyframeTimes(tr Track) []uint64 {
	var out []uint64
	for _, s := range tr.Samples {
		if s.Sync {
			out = append(out, s.DTS)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// FourCC pads and truncates a string to exactly four bytes.
func FourCC(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 4 {
		return s[:4]
	}
	return s + strings.Repeat(" ", 4-len(s))
}
