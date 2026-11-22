// Package fixtures synthesises deterministic ISO-BMFF and audio fixtures.
//
// Everything is generated in Go so tests and the demo never depend on
// external files or network access. The writer emits real stbl tables so the
// production parser, indexer and slicer exercise the same code paths as on
// camera media, and can inject the five documented corruption classes.
package fixtures

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Codec identifies the sample entry emitted for a track.
type Codec string

const (
	CodecAVC Codec = "avc1"
	CodecAAC Codec = "mp4a"
	CodecPCM Codec = "twos"
)

// TrackSpec configures a generated track.
type TrackSpec struct {
	ID         uint32
	Codec      Codec
	Timescale  uint32
	SampleDur  uint32 // media ticks per sample
	Samples    int
	KeyEvery   int     // every Nth sample is a sync sample (1-based)
	SampleSize int     // payload bytes per sample
	EditShift  int64   // empty edit in media ticks prepended to the track
	EditMedia  int64   // media time the edit starts at
	NonZeroCTO int64   // constant composition offset, 0 disables ctts
	MediaStart int     // index of the first sample written into mdat
	MediaOnly  int     // if >0, write only this many samples to mdat
	Silence    []Range // audio intervals (in media ticks) of silence
	SampleSeed uint64
}

// Range is a half-open media-tick interval.
type Range struct{ Start, End int64 }

// FileSpec configures a generated MP4.
type FileSpec struct {
	Timescale uint32 // movie timescale
	MoovFirst bool
	Major     string
	Brands    []string
	Tracks    []TrackSpec
	UseCo64   bool
	// Corruption knobs.
	TruncateAt    int64 // file is cut at this absolute offset, 0 disables
	BreakStszAt   int   // sample index whose stsz entry is corrupted, -1 disables
	BreakSizeOf   int   // sample index whose payload is cut short, -1 disables
	DropSyncAfter int   // remove stss entries after this sample number, 0 disables
	WrapAt        int   // sample index after which the 32-bit clock jumps, 0 disables
}

// AudioSamples generates a deterministic PCM16LE mono waveform at 48 kHz.
// Silent ranges carry exact zero; speech-like ranges carry a seeded tone.
func AudioSamples(sp TrackSpec) []int16 {
	if sp.Samples == 0 {
		return nil
	}
	out := make([]int16, sp.Samples)
	x := sp.SampleSeed | 1
	for i := range out {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		pos := int64(i) * int64(sp.SampleDur)
		silent := false
		for _, r := range sp.Silence {
			if pos >= r.Start && pos < r.End {
				silent = true
				break
			}
		}
		if silent {
			out[i] = 0
			continue
		}
		// Full-scale-ish square-tone bursts so window RMS is unambiguous.
		if (x>>33)&1 == 1 {
			out[i] = 12000
		} else {
			out[i] = -12000
		}
	}
	return out
}

// Keyframe returns whether sample n (0-based) is a sync sample.
func Keyframe(sp TrackSpec, n int) bool {
	if sp.KeyEvery <= 0 {
		return n == 0
	}
	return n%sp.KeyEvery == 0
}

// mediaData builds mdat payloads and per-track sample offsets/sizes.
type mediaData struct {
	payload []byte
	tracks  []trackLayout
}

type trackLayout struct {
	sizes   []uint32
	offsets []int64 // absolute file offsets
}

// BuildMedia lays out every track's samples sequentially.
func BuildMedia(spec FileSpec, mdatStart int64) ([]byte, []trackLayout) {
	var buf []byte
	layouts := make([]trackLayout, len(spec.Tracks))
	for ti, tr := range spec.Tracks {
		payloads := trackPayloads(tr)
		for i, p := range payloads {
			if tr.MediaStart > 0 && i < tr.MediaStart {
				continue
			}
			if tr.MediaOnly > 0 && i >= tr.MediaStart+tr.MediaOnly {
				break
			}
			layouts[ti].offsets = append(layouts[ti].offsets, mdatStart+int64(len(buf)))
			layouts[ti].sizes = append(layouts[ti].sizes, uint32(len(p)))
			buf = append(buf, p...)
		}
	}
	return buf, layouts
}

func trackPayloads(sp TrackSpec) [][]byte {
	out := make([][]byte, sp.Samples)
	if sp.Codec == CodecPCM {
		pcm := AudioSamples(sp)
		per := 1
		if sp.SampleSize > 0 {
			per = sp.SampleSize / 2
			if per < 1 {
				per = 1
			}
		}
		_ = per
		for i := range out {
			b := make([]byte, 2)
			binary.LittleEndian.PutUint16(b, uint16(pcm[i]))
			out[i] = b
		}
		return out
	}
	x := sp.SampleSeed | 1
	size := sp.SampleSize
	if size <= 0 {
		size = 256
	}
	if sp.Codec == CodecAAC && size < 32 {
		size = 32
	}
	for i := range out {
		b := make([]byte, size)
		for j := range b {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
			b[j] = byte(x >> 24)
			if j >= 4 {
				b[j] = b[j-4] // compressible yet content-dependent
			}
		}
		// Vary the first bytes so sample payload hashes differ per index.
		binary.BigEndian.PutUint32(b, uint32(i+1)*2654435761)
		if sp.Codec == CodecAAC {
			b[0] = 0xFF // ADTS sync
			b[1] = 0xF1
			for j := 9; j < len(b); j++ {
				b[j] = 0
			}
			pos := int64(i) * int64(sp.SampleDur)
			for _, r := range sp.Silence {
				if pos >= r.Start && pos < r.End {
					continue
				}
				// Audible frames carry non-zero spectral bytes.
				for j := 9; j < len(b); j++ {
					b[j] = byte((i*31 + j*7) & 0xFF)
					if b[j] == 0 {
						b[j] = 1
					}
				}
				break
			}
		}
		out[i] = b
	}
	return out
}

// box helpers ---------------------------------------------------------------

func box(typ string, payload ...[]byte) []byte {
	n := 8
	for _, p := range payload {
		n += len(p)
	}
	b := make([]byte, 8, n)
	binary.BigEndian.PutUint32(b, uint32(n))
	copy(b[4:], typ)
	for _, p := range payload {
		b = append(b, p...)
	}
	return b
}

func fullBox(typ string, version byte, flags uint32, payload ...[]byte) []byte {
	h := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(typ, append([][]byte{h}, payload...)...)
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }
func u16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }

// Build assembles a complete file. Chunk offsets and the mdat header
// position are solved together: moov-first files shift mdat as moov grows,
// moov-last files keep mdat right after the prefix.
func Build(spec FileSpec) []byte {
	ftyp := box("ftyp", []byte(pad4(spec.Major)), u32(0x200), brands(spec.Brands))
	free := box("free", make([]byte, 8))
	moovFirst := spec.MoovFirst
	var moov []byte
	mdatStart := int64(len(ftyp)+len(free)) + 8
	for i := 0; i < 4; i++ {
		_, layouts := BuildMedia(spec, mdatStart)
		moov = buildMoov(spec, layouts)
		if moovFirst {
			next := int64(len(ftyp)+len(free)+len(moov)) + 8
			if next == mdatStart {
				break
			}
			mdatStart = next
			continue
		}
		break
	}
	media, _ := BuildMedia(spec, mdatStart)
	var out []byte
	if moovFirst {
		out = cat(ftyp, free, moov, box("mdat", media))
	} else {
		out = cat(ftyp, free, box("mdat", media), moov)
	}
	if spec.TruncateAt > 0 && int64(len(out)) > spec.TruncateAt {
		out = out[:spec.TruncateAt]
	}
	return out
}

func pad4(s string) string {
	for len(s) < 4 {
		s += " "
	}
	if len(s) > 4 {
		s = s[:4]
	}
	return s
}

func brands(b []string) []byte {
	var out []byte
	for _, x := range b {
		out = append(out, []byte(pad4(x))...)
	}
	return out
}

func buildMoov(spec FileSpec, layouts []trackLayout) []byte {
	var traks [][]byte
	var maxDur uint64
	for ti, tr := range spec.Tracks {
		layout := trackLayout{}
		if ti < len(layouts) {
			layout = layouts[ti]
		}
		trak, dur := buildTrak(spec, tr, layout)
		traks = append(traks, trak)
		if dur > maxDur {
			maxDur = dur
		}
	}
	mvhd := fullBox("mvhd", 0, 0,
		u32(0), u32(0), u32(spec.Timescale), u32(uint32(maxDur)),
		u32(0x00010000), u16(0x0100), u16(0), u32(0), u32(0),
		matrix(), make([]byte, 24), u32(uint32(len(spec.Tracks)+1)))
	return box("moov", append([][]byte{mvhd}, traks...)...)
}

func matrix() []byte {
	vals := []uint32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
	var out []byte
	for _, v := range vals {
		out = append(out, u32(v)...)
	}
	return out
}

func buildTrak(spec FileSpec, tr TrackSpec, layout trackLayout) ([]byte, uint64) {
	durMedia := uint64(tr.Samples) * uint64(tr.SampleDur)
	tkhdDur := durMedia * uint64(spec.Timescale) / uint64(tr.Timescale)
	if tr.EditShift > 0 {
		tkhdDur += uint64(tr.EditShift) * uint64(spec.Timescale) / uint64(tr.Timescale)
	}
	var vol uint16
	if tr.Codec == CodecAAC || tr.Codec == CodecPCM {
		vol = 0x0100
	}
	tkhd := fullBox("tkhd", 0, 7, u32(0), u32(0), u32(tr.ID), u32(0), u32(uint32(tkhdDur)),
		u32(0), u32(0), u16(0), u16(0), u16(vol), u16(0), matrix(), u32(0), u32(0))
	mdhd := fullBox("mdhd", 0, 0, u32(0), u32(0), u32(tr.Timescale), u32(uint32(durMedia)), u16(0x55c4), u16(0))
	var hdlrType string
	var handlerName string
	switch tr.Codec {
	case CodecAAC, CodecPCM:
		hdlrType, handlerName = "soun", "SoundHandler\x00"
	default:
		hdlrType, handlerName = "vide", "VideoHandler\x00"
	}
	hdlr := fullBox("hdlr", 0, 0, u32(0), []byte(hdlrType), make([]byte, 12), []byte(handlerName))

	edts := []byte(nil)
	if tr.EditShift > 0 || tr.EditMedia > 0 {
		emptyDur := uint64(tr.EditShift) * uint64(spec.Timescale) / uint64(tr.Timescale)
		mediaDur := durMedia * uint64(spec.Timescale) / uint64(tr.Timescale)
		elst := fullBox("elst", 0, 0, u32(2),
			u32(uint32(emptyDur)), u32(0xFFFFFFFF), u16(1), u16(0),
			u32(uint32(mediaDur)), u32(uint32(tr.EditMedia)), u16(1), u16(0))
		edts = box("edts", elst)
	}

	stbl := buildStbl(spec, tr, layout)
	minf := box("minf",
		fullBox("vmhd", 0, 1, make([]byte, 8)),
		box("dinf", fullBox("dref", 0, 0, u32(1), fullBox("url ", 0, 1))),
		stbl)
	mdia := box("mdia", mdhd, hdlr, minf)
	return box("trak", tkhd, edts, mdia), tkhdDur
}

func buildStbl(spec FileSpec, tr TrackSpec, layout trackLayout) []byte {
	n := len(layout.sizes)
	writeCount := n
	if tr.MediaOnly > 0 {
		writeCount = tr.MediaOnly
	}
	stsd := buildStsd(tr)

	stts := buildStts(spec, tr)

	// stsc: one sample per chunk.
	var stscEntries []byte
	chunks := writeCount
	entryCount := uint32(0)
	if chunks > 0 {
		entryCount = 1
		stscEntries = cat(u32(1), u32(1), u32(1))
	}
	stsc := fullBox("stsc", 0, 0, u32(entryCount), stscEntries)

	// stsz with optional corrupted entry.
	sz := make([]byte, 0, writeCount*4)
	for i := 0; i < writeCount; i++ {
		v := layout.sizes[i]
		if i == spec.BreakStszAt {
			v = 0x00FFFFFF // claims far more than the file holds
		}
		if i == spec.BreakSizeOf {
			v = uint32(math.MaxUint32/2) + v
		}
		sz = append(sz, u32(v)...)
	}
	stsz := fullBox("stsz", 0, 0, u32(0), u32(uint32(writeCount)), sz)

	var stco []byte
	if spec.UseCo64 {
		var offs []byte
		for _, o := range layout.offsets {
			offs = append(offs, u64(uint64(o))...)
		}
		stco = fullBox("co64", 0, 0, u32(uint32(writeCount)), offs)
	} else {
		var offs []byte
		for _, o := range layout.offsets {
			offs = append(offs, u32(uint32(o))...)
		}
		stco = fullBox("stco", 0, 0, u32(uint32(writeCount)), offs)
	}

	parts := [][]byte{stsd, stts, stsc, stsz, stco}
	if tr.Codec != CodecPCM {
		var syncs []byte
		count := uint32(0)
		for i := 0; i < tr.Samples; i++ {
			if spec.DropSyncAfter > 0 && i >= spec.DropSyncAfter {
				continue
			}
			if Keyframe(tr, i) {
				count++
				syncs = append(syncs, u32(uint32(i+1))...)
			}
		}
		if tr.KeyEvery > 0 {
			parts = append(parts, fullBox("stss", 0, 0, u32(count), syncs))
		}
	}
	if tr.NonZeroCTO != 0 {
		ctts := fullBox("ctts", 0, 0, u32(1), u32(uint32(tr.Samples)), u32(uint32(tr.NonZeroCTO)))
		parts = append(parts, ctts)
	}
	return box("stbl", parts...)
}

// buildStts emits constant-duration runs, injecting two 2^31 jumps at WrapAt
// so a 32-bit media clock crosses 2^32 and wraps back to a small value.
func buildStts(spec FileSpec, tr TrackSpec) []byte {
	if spec.WrapAt <= 0 || spec.WrapAt >= tr.Samples-1 {
		return fullBox("stts", 0, 0, u32(1), u32(uint32(tr.Samples)), u32(tr.SampleDur))
	}
	var entries []byte
	entries = append(entries, u32(uint32(spec.WrapAt))...)
	entries = append(entries, u32(tr.SampleDur)...)
	entries = append(entries, u32(2)...)
	entries = append(entries, u32(0x80000000)...)
	rest := tr.Samples - spec.WrapAt - 2
	if rest > 0 {
		entries = append(entries, u32(uint32(rest))...)
		entries = append(entries, u32(tr.SampleDur)...)
	}
	count := uint32(2)
	if rest > 0 {
		count = 3
	}
	return fullBox("stts", 0, 0, u32(count), entries)
}

func buildStsd(tr TrackSpec) []byte {
	var entry []byte
	switch tr.Codec {
	case CodecPCM:
		// PCM sample entry: version 0, 2 channels, 16-bit, 48 kHz.
		body := cat(make([]byte, 6), u16(1), u16(0), u16(0), u32(0),
			u16(2), u16(16), u16(0), u16(0), u32(48000<<16))
		entry = box("twos", body)
	case CodecAAC:
		esds := fullBox("esds", 0, 0,
			[]byte{0x03, 0x19, 0x00, 0x01, 0x00, 0x04, 0x11, 0x40, 0x15,
				0x00, 0x00, 0x00, 0x00, 0x01, 0xf4, 0x00, 0x00, 0x01, 0xf4,
				0x00, 0x05, 0x02, 0x12, 0x10, 0x06, 0x01, 0x02})
		body := cat(make([]byte, 6), u16(1), u16(0), u16(0), u32(0),
			u16(2), u16(16), u16(0), u16(0), u32(48000<<16), esds)
		entry = box("mp4a", body)
	default:
		body := cat(make([]byte, 6), u16(1), u16(0), u16(0), u32(0), u32(0),
			u16(640), u16(360), u32(0x00480000), u32(0x00480000), u32(0),
			u16(1), make([]byte, 32), u16(0x0018), u16(0xFFFF))
		entry = box("avc1", body)
	}
	return fullBox("stsd", 0, 0, u32(1), entry)
}

// BuildPCMFile emits a minimal movie whose single audio track carries raw
// little-endian PCM samples, optionally with an imported silence layout.
func BuildPCMFile(samples int, silence []Range, sampleDur uint32) []byte {
	return Build(FileSpec{
		Timescale: 1000, MoovFirst: false, Major: "isom", Brands: []string{"isom", "qt  "},
		BreakStszAt: -1, BreakSizeOf: -1,
		Tracks: []TrackSpec{{
			ID: 1, Codec: CodecPCM, Timescale: 48000, SampleDur: sampleDur,
			Samples: samples, SampleSize: 2, KeyEvery: 1, Silence: silence, SampleSeed: 77,
		}},
	})
}

// Describe returns a stable one-line summary for test failure messages.
func Describe(spec FileSpec) string {
	return fmt.Sprintf("tracks=%d moovFirst=%v", len(spec.Tracks), spec.MoovFirst)
}
