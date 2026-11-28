// Package media generates the self-contained MP4 and RTP fixtures used by the
// regression demo and tests. Nothing here reads from disk or the network.
package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
)

// Damage selects an intentional defect in a generated MP4.
type Damage int

const (
	DamageNone Damage = iota
	// DamageTruncate cuts the file in the middle of the mdat payload.
	DamageTruncate
	// DamageBadIndex omits the chunk-offset table so the index must be
	// recovered by scanning media data.
	DamageBadIndex
	// DamageNoSizes omits the sample-size table, which cannot be recovered.
	DamageNoSizes
	// DamageBadStts declares more timing entries than are present.
	DamageBadStts
)

func (d Damage) String() string {
	switch d {
	case DamageTruncate:
		return "truncated"
	case DamageBadIndex:
		return "bad_index"
	case DamageNoSizes:
		return "missing_sizes"
	case DamageBadStts:
		return "bad_timing"
	default:
		return "none"
	}
}

// SampleSpec describes a contiguous run of coded samples.
type SampleSpec struct {
	Count    int
	Size     uint32
	Delta    uint32
	KeyEvery int // 0 means every sample is a key sample
}

// TrackSpec describes one track to mux.
type TrackSpec struct {
	ID         uint32
	Handler    string // "vide" or "soun"
	Timescale  uint32
	StartTicks int64 // first decode timestamp (non-zero simulates late start)
	EditMedia  int64 // edit-list media_time, in media ticks
	EditDur    int64 // media edit segment_duration, 0 meaning "to end"
	// EmptyEditTicks prepends an empty edit (media_time = -1) of this many
	// presentation ticks, delaying the track without consuming media.
	EmptyEditTicks int64
	Samples        SampleSpec
}

// FileSpec is a complete synthetic asset rendition.
type FileSpec struct {
	Asset  string
	Damage Damage
	Tracks []TrackSpec
}

// TrackBytes returns the encoded payload size of a track's samples.
func (t TrackSpec) TrackBytes() int64 {
	return int64(t.Samples.Count) * int64(t.Samples.Size)
}

func box(typ string, payload ...[]byte) []byte {
	n := 8
	for _, p := range payload {
		n += len(p)
	}
	out := make([]byte, 8, n)
	binary.BigEndian.PutUint32(out[:4], uint32(n))
	copy(out[4:8], typ)
	for _, p := range payload {
		out = append(out, p...)
	}
	return out
}

func fullBox(typ string, version byte, flags uint32, payload ...[]byte) []byte {
	n := 12
	for _, p := range payload {
		n += len(p)
	}
	out := make([]byte, 12, n)
	binary.BigEndian.PutUint32(out[:4], uint32(n))
	copy(out[4:8], typ)
	out[8] = version
	out[9] = byte(flags >> 16)
	out[10] = byte(flags >> 8)
	out[11] = byte(flags)
	for _, p := range payload {
		out = append(out, p...)
	}
	return out
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// SampleBytes returns the deterministic encoded bytes of one sample. The
// content depends on asset, track and sample index so digests are stable
// across runs and differ between renditions.
func SampleBytes(asset string, track uint32, index int, size uint32) []byte {
	out := make([]byte, size)
	h := fnv.New64a()
	fmt.Fprintf(h, "%s/%d/%d", asset, track, index)
	seed := h.Sum64()
	for i := range out {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		out[i] = byte(seed)
	}
	return out
}

// BuildFile muxes a synthetic MP4. The layout is ftyp, moov, mdat with all
// samples of a track packed into a single chunk, which is enough to exercise
// the scanner, index recovery and edit-list handling.
func BuildFile(spec FileSpec) ([]byte, error) {
	if len(spec.Tracks) == 0 {
		return nil, errors.New("media: file spec has no tracks")
	}
	ftyp := box("ftyp", []byte("isom"), u32(512), []byte("isomiso2avc1mp41"))
	var traks [][]byte
	for _, t := range spec.Tracks {
		if t.Timescale == 0 || t.Samples.Count <= 0 || t.Samples.Size == 0 || t.Samples.Delta == 0 {
			return nil, fmt.Errorf("media: invalid track spec %+v", t)
		}
		traks = append(traks, buildTrak(t))
	}
	mvhd := buildMvhd(uint32(len(spec.Tracks) + 1))
	moov := box("moov", append([][]byte{mvhd}, traks...)...)

	// Lay out mdat right after moov. All stco entries initially hold zero;
	// patch them once the real payload offset is known.
	mdatHeader := make([]byte, 8)
	copy(mdatHeader[4:], "mdat")
	payloadStart := int64(len(ftyp) + len(moov) + 8)

	var payload []byte
	offsets := make([]int64, len(spec.Tracks))
	for i, t := range spec.Tracks {
		offsets[i] = payloadStart + int64(len(payload))
		for j := 0; j < t.Samples.Count; j++ {
			payload = append(payload, SampleBytes(spec.Asset, t.ID, j, t.Samples.Size)...)
		}
	}
	moov = patchChunkOffsets(moov, offsets)
	total := int64(len(ftyp) + len(moov) + 8 + len(payload))
	binary.BigEndian.PutUint32(mdatHeader[:4], uint32(8+len(payload)))

	out := make([]byte, 0, total)
	out = append(out, ftyp...)
	out = append(out, moov...)
	out = append(out, mdatHeader...)
	out = append(out, payload...)

	if spec.Damage == DamageTruncate {
		cut := len(ftyp) + len(moov) + 8 + len(payload)/2
		out = out[:cut]
	}
	return out, nil
}

func buildMvhd(nextTrack uint32) []byte {
	body := append(u32(0), u32(0)...)       // creation, modification
	body = append(body, u32(1000)...)       // timescale
	body = append(body, u32(0)...)          // duration (unknown is fine here)
	body = append(body, u32(0x00010000)...) // rate
	body = append(body, []byte{1, 0, 0, 0}...)
	body = append(body, u32(0)...)
	body = append(body, u32(0)...)
	matrix := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	for _, v := range matrix {
		body = append(body, u32(v)...)
	}
	body = append(body, make([]byte, 24)...)
	body = append(body, u32(nextTrack)...)
	return fullBox("mvhd", 0, 0, body)
}

func buildTrak(t TrackSpec) []byte {
	tkhd := buildTkhd(t)
	elst := buildElst(t)
	mdhd := buildMdhd(t)
	hdlr := buildHdlr(t)
	minf := buildMinf(t)
	mdia := box("mdia", mdhd, hdlr, minf)
	return box("trak", tkhd, box("edts", elst), mdia)
}

func buildTkhd(t TrackSpec) []byte {
	body := append(u32(0), u32(0)...)
	body = append(body, u32(t.ID)...)
	body = append(body, u32(0)...)
	body = append(body, u32(0)...)
	body = append(body, make([]byte, 8)...)
	body = append(body, make([]byte, 8)...) // layer, alternate group, volume, reserved
	matrix := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	for _, v := range matrix {
		body = append(body, u32(v)...)
	}
	body = append(body, u32(0)...)
	body = append(body, u32(0)...)
	return fullBox("tkhd", 0, 3, body)
}

func buildElst(t TrackSpec) []byte {
	entry := func(dur int64, media int64) []byte {
		e := append(u32(uint32(dur)), u32(uint32(int32(media)))...)
		return append(e, u32(0x00010000)...)
	}
	if t.EmptyEditTicks > 0 {
		body := append(u32(2), entry(t.EmptyEditTicks, -1)...)
		body = append(body, entry(t.EditDur, t.EditMedia)...)
		return fullBox("elst", 0, 0, body)
	}
	body := append(u32(1), entry(t.EditDur, t.EditMedia)...)
	return fullBox("elst", 0, 0, body)
}

func buildMdhd(t TrackSpec) []byte {
	body := append(u32(0), u32(0)...)
	body = append(body, u32(t.Timescale)...)
	dur := uint32(int64(t.Samples.Count) * int64(t.Samples.Delta))
	body = append(body, u32(dur)...)
	// ISO-639-2 "und" packed into 16 bits, then pre_defined = 0.
	body = append(body, []byte{0x55, 0xc4, 0, 0}...)
	return fullBox("mdhd", 0, 0, body)
}

func buildHdlr(t TrackSpec) []byte {
	body := append(u32(0), []byte(t.Handler)...)
	body = append(body, make([]byte, 12)...)
	body = append(body, []byte("mediapipe\x00")...)
	return fullBox("hdlr", 0, 0, body)
}

func buildMinf(t TrackSpec) []byte {
	var mhd []byte
	if t.Handler == "soun" {
		mhd = fullBox("smhd", 0, 0, make([]byte, 4))
	} else {
		mhd = fullBox("vmhd", 0, 1, make([]byte, 8))
	}
	dref := fullBox("dref", 0, 0, u32(1), fullBox("url ", 0, 1))
	dinf := box("dinf", dref)
	return box("minf", mhd, dinf, buildStbl(t))
}

func buildStbl(t TrackSpec) []byte {
	stsd := fullBox("stsd", 0, 0, u32(0))
	var parts [][]byte
	parts = append(parts, stsd)
	stts := sttsPayload(t.Samples.Count, t.Samples.Delta)
	switch t.Samples.Count {
	case 0:
	default:
		parts = append(parts, fullBox("stts", 0, 0, u32(1), stts))
	}
	if t.KeyEvery() > 0 && t.Handler == "vide" {
		parts = append(parts, buildStss(t.Samples))
	}
	parts = append(parts, buildStsz(t.Samples))
	parts = append(parts, fullBox("stsc", 0, 0, u32(1), u32(1), u32(uint32(t.Samples.Count)), u32(1)))
	parts = append(parts, buildStco(t.Samples.Count))
	return box("stbl", parts...)
}

// KeyEvery returns the effective keyframe interval in samples.
func (t TrackSpec) KeyEvery() int {
	if t.Samples.KeyEvery <= 0 || t.Handler != "vide" {
		return 0
	}
	return t.Samples.KeyEvery
}

func sttsPayload(count int, delta uint32) []byte {
	return append(u32(uint32(count)), u32(delta)...)
}

func buildStss(s SampleSpec) []byte {
	var nums []uint32
	for i := 0; i < s.Count; i++ {
		if i%s.KeyEvery == 0 {
			nums = append(nums, uint32(i+1))
		}
	}
	body := u32(uint32(len(nums)))
	for _, n := range nums {
		body = append(body, u32(n)...)
	}
	return fullBox("stss", 0, 0, body)
}

func buildStsz(s SampleSpec) []byte {
	return fullBox("stsz", 0, 0, u32(s.Size), u32(uint32(s.Count)))
}

func buildStco(count int) []byte {
	return fullBox("stco", 0, 0, u32(1), u32(0))
}

var errPatch = errors.New("media: chunk offset table not found")

// patchChunkOffsets rewrites the single stco entry of each trak, in order,
// with the given absolute offsets.
func patchChunkOffsets(moov []byte, offsets []int64) []byte {
	pos := 0
	for _, off := range offsets {
		idx := findBox(moov, pos, "stco")
		if idx < 0 {
			continue
		}
		// stco: size(4) type(4) version/flags(4) count(4) entry(4)
		binary.BigEndian.PutUint32(moov[idx+16:idx+20], uint32(off))
		pos = idx + 20
	}
	return moov
}

func findBox(data []byte, from int, typ string) int {
	for i := from; i+8 <= len(data); i++ {
		if string(data[i+4:i+8]) != typ {
			continue
		}
		size := int(binary.BigEndian.Uint32(data[i : i+4]))
		if size >= 8 && i+size <= len(data) {
			return i
		}
	}
	return -1
}

// ManifestRanges splits a track's sample run into logical ranges of at most
// segSamples samples each, returning summaries used for cross-version merge.
func ManifestRanges(asset string, t TrackSpec, segSamples int) []RangeSpec {
	if segSamples <= 0 {
		segSamples = t.Samples.Count
	}
	var out []RangeSpec
	for start := 0; start < t.Samples.Count; start += segSamples {
		end := start + segSamples
		if end > t.Samples.Count {
			end = t.Samples.Count
		}
		h := fnv.New64a()
		fmt.Fprintf(h, "%s/%d/%d-%d", asset, t.ID, start, end)
		out = append(out, RangeSpec{
			StartTicks:    t.StartTicks + int64(start)*int64(t.Samples.Delta),
			DurationTicks: int64(end-start) * int64(t.Samples.Delta),
			Summary:       fmt.Sprintf("%016x", h.Sum64()),
			Samples:       end - start,
			Bytes:         int64(end-start) * int64(t.Samples.Size),
			Sparse:        t.KeyEvery() > 0 && end-start > t.KeyEvery(),
			GopTicks:      int64(t.KeyEvery()) * int64(t.Samples.Delta),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartTicks < out[j].StartTicks })
	return out
}

// StripTable zeroes the payload of every box of the given type in place,
// descending into containers, and leaves all box headers intact. It is used to
// generate damaged-index fixtures: the container still scans, but the table is
// unusable and must be recovered or rejected. It returns how many boxes were
// touched.
func StripTable(data []byte, typ string) int {
	return stripRange(data, 0, len(data), typ)
}

func stripRange(data []byte, start, end int, typ string) int {
	n := 0
	for i := start; i+8 <= end; {
		size := int(binary.BigEndian.Uint32(data[i : i+4]))
		if size < 8 || i+size > end {
			return n
		}
		t := string(data[i+4 : i+8])
		if t == typ {
			for j := i + 8; j < i+size; j++ {
				data[j] = 0
			}
			n++
		} else if isContainerType(t) {
			n += stripRange(data, i+8, i+size, typ)
		}
		i += size
	}
	return n
}

func isContainerType(t string) bool {
	switch t {
	case "moov", "trak", "mdia", "minf", "stbl", "edts", "mvex", "moof", "traf", "dinf", "udta":
		return true
	}
	return false
}

// RangeSpec mirrors manifest.Range without importing it, keeping this package
// free of upward dependencies.
type RangeSpec struct {
	StartTicks    int64
	DurationTicks int64
	Summary       string
	Samples       int
	Bytes         int64
	Sparse        bool
	GopTicks      int64
}
