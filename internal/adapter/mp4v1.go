// Package adapter: MP4 adapter, major version 1.
//
// The adapter parses progressive MP4 sample tables into a media-neutral form:
// per-sample durations, sync-sample flags, chunk offsets, and codec
// configuration. It is deliberately strict:
//
//   - only browser/editing-relevant codecs (H.264, AAC) are supported, and an
//     unsupported codec fails the parse before any output is produced;
//   - every sample's byte range is checked against the input buffer, and a
//     truncated sample is reported with the byte offset where its data should
//     have started;
//   - all reads are bounded by the input slice and all errors carry the
//     adapter stage plus a byte offset.
package adapter

import (
	"bytes"
	"sort"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/mp4"

	"example.com/mediaforge/internal/merrors"
)

// MP4V1 is the major version implemented here.
const MP4V1 Major = 1

// MaxMP4Size bounds the container accepted by the adapter.
const MaxMP4Size = 16 << 20

// MaxSamples bounds per-track sample count.
const MaxSamples = 200_000

// MP4Adapter parses and rewrites progressive MP4.
type MP4Adapter struct{}

// NewMP4Adapter creates the adapter.
func NewMP4Adapter() *MP4Adapter { return &MP4Adapter{} }

// Key reports the format and version.
func (a *MP4Adapter) Key() Key { return Key{Format: FormatMP4, Major: MP4V1} }

// Sniff reports whether buf starts with an ftyp or moov box.
func (a *MP4Adapter) Sniff(buf []byte) bool {
	if len(buf) < 8 {
		return false
	}
	t := string(buf[4:8])
	return t == "ftyp" || t == "moov" || t == "styp"
}

// Parse decodes a progressive MP4 file. The returned container holds every
// sample's data, duration, sync flag, and source offset.
func (a *MP4Adapter) Parse(buf []byte) (ParseResult, error) {
	if len(buf) > MaxMP4Size {
		return ParseResult{}, merrors.At(merrors.StageAdapter, merrors.Bounds, int64(len(buf)),
			"mp4 size %d exceeds limit", len(buf))
	}
	if !a.Sniff(buf) {
		return ParseResult{}, merrors.At(merrors.StageAdapter, merrors.Malformed, 0,
			"not an mp4 file (no ftyp/moov)")
	}
	// Lazy mdat: the adapter reads sample bytes itself from buf, and this
	// mode keeps mp4ff from rejecting a structurally complete file whose
	// mdat is shorter than its header advertises. That case must surface as
	// a per-sample truncation warning, not a blanket decode failure.
	f, err := mp4.DecodeFile(bytes.NewReader(buf), mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return ParseResult{}, merrors.Wrap(merrors.StageAdapter, merrors.Malformed, 0, err,
			"mp4 decode")
	}
	if f.Moov == nil {
		return ParseResult{}, merrors.At(merrors.StageAdapter, merrors.Malformed, 0, "no moov box")
	}

	c := &Container{Format: FormatMP4, Major: MP4V1, Samples: make(map[uint32][]Sample), Data: buf}
	var warnings []error
	for _, trak := range f.Moov.Traks {
		info, samples, warns, err := a.parseTrak(trak, buf)
		if err != nil {
			return ParseResult{}, err
		}
		warnings = append(warnings, warns...)
		c.Tracks = append(c.Tracks, info)
		c.Samples[info.ID] = samples
	}
	if len(c.Tracks) == 0 {
		return ParseResult{}, merrors.At(merrors.StageAdapter, merrors.Empty, 0, "mp4 has no tracks")
	}
	return ParseResult{Container: c, Warnings: warnings}, nil
}

func (a *MP4Adapter) parseTrak(trak *mp4.TrakBox, buf []byte) (TrackInfo, []Sample, []error, error) {
	var info TrackInfo
	if trak.Tkhd == nil || trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil {
		return info, nil, nil, merrors.At(merrors.StageAdapter, merrors.Malformed, 0,
			"incomplete trak structure")
	}
	stbl := trak.Mdia.Minf.Stbl
	info.ID = trak.Tkhd.TrackID
	if trak.Mdia.Mdhd == nil {
		return info, nil, nil, merrors.At(merrors.StageAdapter, merrors.Malformed, 0,
			"trak %d has no mdhd", info.ID)
	}
	info.Timescale = trak.Mdia.Mdhd.Timescale
	if info.Timescale == 0 {
		return info, nil, nil, merrors.At(merrors.StageAdapter, merrors.Invalid, 0,
			"trak %d timescale is zero", info.ID)
	}

	codec, cfg, err := codecFromStsd(stbl.Stsd)
	if err != nil {
		return info, nil, nil, err
	}
	info.Codec = codec
	info.CodecConfig = cfg
	if !Supports(FormatMP4, codec) {
		return info, nil, nil, merrors.At(merrors.StageAdapter, merrors.Unsupported, 0,
			"trak %d codec %q is not supported", info.ID, codec)
	}

	if stbl.Stts == nil || stbl.Stsc == nil || stbl.Stsz == nil {
		return info, nil, nil, merrors.At(merrors.StageAdapter, merrors.Malformed, 0,
			"trak %d missing sample table", info.ID)
	}
	nr := int(stbl.Stsz.GetNrSamples())
	if nr == 0 {
		return info, nil, nil, merrors.At(merrors.StageAdapter, merrors.Empty, 0,
			"trak %d has no samples", info.ID)
	}
	if nr > MaxSamples {
		return info, nil, nil, merrors.At(merrors.StageAdapter, merrors.Bounds, int64(nr),
			"trak %d has %d samples, limit %d", info.ID, nr, MaxSamples)
	}

	samples := make([]Sample, nr)
	info.Durations = make([]uint64, nr)
	for i := 0; i < nr; i++ {
		d := uint64(stbl.Stts.GetDur(uint32(i + 1)))
		info.Durations[i] = d
		samples[i].Index = i
		samples[i].Duration = d
		if stbl.Stss == nil || stbl.Stss.IsSyncSample(uint32(i+1)) {
			samples[i].IsKeyframe = true
			info.Keyframes = append(info.Keyframes, i)
		}
	}
	info.SampleCnt = nr

	// Walk the chunk table to compute each sample's byte offset.
	chunks, err := stbl.Stsc.GetContainingChunks(1, uint32(nr))
	if err != nil {
		return info, nil, nil, merrors.Wrap(merrors.StageAdapter, merrors.Malformed, 0, err,
			"trak %d chunk table", info.ID)
	}
	offsetForChunk := func(chunkNr uint32) (uint64, error) {
		if stbl.Stco != nil {
			return stbl.Stco.GetOffset(int(chunkNr))
		}
		if stbl.Co64 != nil {
			return stbl.Co64.GetOffset(int(chunkNr))
		}
		return 0, merrors.At(merrors.StageAdapter, merrors.Malformed, 0,
			"trak %d has no chunk offsets", info.ID)
	}
	var warnings []error
	n := 0
	for _, ch := range chunks {
		off, err := offsetForChunk(ch.ChunkNr)
		if err != nil {
			return info, nil, nil, err
		}
		for k := uint32(0); k < ch.NrSamples && n < nr; k++ {
			// mp4ff's sample-size accessor is one-based and panics at zero.
			size := uint64(stbl.Stsz.GetSampleSize(n + 1))
			if size == 0 {
				return info, nil, nil, merrors.At(merrors.StageAdapter, merrors.Malformed,
					int64(n), "trak %d sample %d has zero size", info.ID, n+1)
			}
			samples[n].SourceOffset = int64(off)
			start := off
			end := off + size
			if end > uint64(len(buf)) || end < start {
				// The sample table promises data past the end of the input.
				// Keep the metadata so downstream can still see the sample,
				// but report the truncation with the byte offset the data
				// should have started at.
				warnings = append(warnings, merrors.At(merrors.StageAdapter, merrors.Truncated,
					int64(start), "trak %d sample %d: %d bytes at offset %d, file has %d",
					info.ID, n+1, size, start, len(buf)))
				samples[n].Data = nil
			} else {
				samples[n].Data = buf[start:end]
			}
			off = end
			n++
		}
	}
	if n != nr {
		return info, nil, nil, merrors.At(merrors.StageAdapter, merrors.Malformed, int64(n),
			"trak %d chunk table covers %d of %d samples", info.ID, n, nr)
	}
	return info, samples, warnings, nil
}

func codecFromStsd(stsd *mp4.StsdBox) (Codec, []byte, error) {
	if stsd == nil {
		return "", nil, merrors.At(merrors.StageAdapter, merrors.Malformed, 0, "no stsd box")
	}
	switch {
	case stsd.AvcX != nil:
		if stsd.AvcX.AvcC == nil {
			return "", nil, merrors.At(merrors.StageAdapter, merrors.Malformed, 0, "avc1 without avcC")
		}
		cfg, err := marshalAvcC(stsd.AvcX.AvcC)
		if err != nil {
			return "", nil, err
		}
		return CodecH264, cfg, nil
	case stsd.Mp4a != nil:
		if stsd.Mp4a.Esds == nil {
			return "", nil, merrors.At(merrors.StageAdapter, merrors.Malformed, 0, "mp4a without esds")
		}
		dsi := stsd.Mp4a.Esds.DecConfigDescriptor.DecSpecificInfo
		if dsi == nil || len(dsi.DecConfig) == 0 {
			return "", nil, merrors.At(merrors.StageAdapter, merrors.Malformed, 0,
				"mp4a without decoder config")
		}
		cfg := append([]byte(nil), dsi.DecConfig...)
		if !aacSupported(cfg) {
			return "", nil, merrors.At(merrors.StageAdapter, merrors.Unsupported, 0,
				"aac decoder config %x", cfg)
		}
		return CodecAAC, cfg, nil
	case stsd.HvcX != nil:
		return Codec("h265"), nil, nil
	case stsd.VpXX != nil:
		return Codec("vp9"), nil, nil
	}
	return "", nil, merrors.At(merrors.StageAdapter, merrors.Unsupported, 0,
		"unrecognised sample entry")
}

// aacSupported reports whether a raw AudioSpecificConfig names an object type
// the mp4 v1 writer can express.
func aacSupported(cfg []byte) bool {
	if len(cfg) < 2 {
		return false
	}
	cfgObj, err := aac.DecodeAudioSpecificConfig(bytes.NewReader(cfg))
	if err != nil {
		return false
	}
	switch cfgObj.ObjectType {
	case aac.AAClc, aac.HEAACv1, aac.HEAACv2:
		return true
	}
	return false
}

func marshalAvcC(c *mp4.AvcCBox) ([]byte, error) {
	var b bytes.Buffer
	if err := c.Encode(&b); err != nil {
		return nil, merrors.Wrap(merrors.StageAdapter, merrors.Malformed, 0, err, "avcC encode")
	}
	// The decoder config record is the avcC payload without its 8-byte header.
	raw := b.Bytes()
	if len(raw) < 8 {
		return nil, merrors.At(merrors.StageAdapter, merrors.Malformed, 0, "avcC too short")
	}
	return append([]byte(nil), raw[8:]...), nil
}

// Encode rewrites a container as a progressive MP4. It validates every track's
// codec, timescale, and sample data before constructing any bytes, so a
// failure leaves no partial output.
func (a *MP4Adapter) Encode(c *Container) ([]byte, error) {
	if c == nil {
		return nil, merrors.New(merrors.StageAdapter, merrors.Invalid, "nil container")
	}
	if c.Major != MP4V1 {
		return nil, merrors.New(merrors.StageAdapter, merrors.Unsupported,
			"container major %d not supported by mp4 v%d adapter", c.Major, MP4V1)
	}
	if len(c.Tracks) == 0 {
		return nil, merrors.New(merrors.StageAdapter, merrors.Empty, "no tracks to encode")
	}

	// Validate everything up front.
	type plan struct {
		info    *TrackInfo
		samples []Sample
		total   int
	}
	var plans []plan
	for i := range c.Tracks {
		info := &c.Tracks[i]
		if !Supports(FormatMP4, info.Codec) {
			return nil, merrors.New(merrors.StageAdapter, merrors.Unsupported,
				"trak %d codec %q cannot be written", info.ID, info.Codec)
		}
		if info.Timescale == 0 {
			return nil, merrors.New(merrors.StageAdapter, merrors.Invalid,
				"trak %d timescale is zero", info.ID)
		}
		samples := c.Samples[info.ID]
		if len(samples) == 0 {
			return nil, merrors.New(merrors.StageAdapter, merrors.Empty,
				"trak %d has no samples", info.ID)
		}
		if len(samples) > MaxSamples {
			return nil, merrors.New(merrors.StageAdapter, merrors.Bounds,
				"trak %d has %d samples", info.ID, len(samples))
		}
		total := 0
		for j, s := range samples {
			if s.Duration == 0 {
				return nil, merrors.New(merrors.StageAdapter, merrors.Invalid,
					"trak %d sample %d has zero duration", info.ID, j)
			}
			if len(s.Data) == 0 {
				return nil, merrors.At(merrors.StageAdapter, merrors.Truncated,
					s.SourceOffset, "trak %d sample %d has no data (source offset %d)",
					info.ID, j, s.SourceOffset)
			}
			total += len(s.Data)
		}
		plans = append(plans, plan{info: info, samples: samples, total: total})
	}

	f := mp4.NewFile()
	f.AddChild(mp4.CreateFtyp(), 0)
	moov := mp4.NewMoovBox()
	moov.AddChild(mp4.CreateMvhd())

	var allData []byte
	for _, p := range plans {
		trak, err := buildTrak(p.info, p.samples)
		if err != nil {
			return nil, err
		}
		moov.AddChild(trak)
	}
	moov.AddChild(mp4.NewMvexBox())
	f.AddChild(moov, 0)

	// Data layout is fixed deterministically: tracks in declaration order,
	// samples in index order.
	allData = make([]byte, 0, func() int {
		n := 0
		for _, p := range plans {
			n += p.total
		}
		return n
	}())
	for _, p := range plans {
		for _, s := range p.samples {
			allData = append(allData, s.Data...)
		}
	}
	f.AddChild(&mp4.MdatBox{Data: allData}, 0)

	// Encode once to learn where mdat data lands, then fix offsets and encode
	// again. Layout is stable because neither fixpoint changes byte counts.
	var buf bytes.Buffer
	if err := f.Encode(&buf); err != nil {
		return nil, merrors.Wrap(merrors.StageAdapter, merrors.Malformed, 0, err, "mp4 encode")
	}
	first := buf.Bytes()
	mdatStart := findBoxData(first, "mdat")
	if mdatStart < 0 {
		return nil, merrors.New(merrors.StageAdapter, merrors.Malformed, "encoded file has no mdat")
	}
	dataOff := uint64(mdatStart)
	for _, p := range plans {
		trak := findTrak(f.Moov, p.info.ID)
		if trak == nil {
			return nil, merrors.New(merrors.StageAdapter, merrors.Malformed,
				"trak %d missing from output", p.info.ID)
		}
		stbl := trak.Mdia.Minf.Stbl
		if stbl == nil || stbl.Stco == nil {
			return nil, merrors.New(merrors.StageAdapter, merrors.Malformed,
				"trak %d has no stco", p.info.ID)
		}
		if len(stbl.Stco.ChunkOffset) != 1 {
			return nil, merrors.New(merrors.StageAdapter, merrors.Malformed,
				"trak %d expected one chunk, got %d", p.info.ID, len(stbl.Stco.ChunkOffset))
		}
		stbl.Stco.ChunkOffset[0] = uint32(dataOff)
		dataOff += uint64(p.total)
	}
	buf.Reset()
	if err := f.Encode(&buf); err != nil {
		return nil, merrors.Wrap(merrors.StageAdapter, merrors.Malformed, 0, err, "mp4 re-encode")
	}
	return buf.Bytes(), nil
}

func buildTrak(info *TrackInfo, samples []Sample) (*mp4.TrakBox, error) {
	mediaType := "video"
	if info.Codec == CodecAAC {
		mediaType = "audio"
	}
	trak := mp4.CreateEmptyTrak(info.ID, info.Timescale, mediaType, "und")
	switch info.Codec {
	case CodecH264:
		sps, pps, err := avcParams(info.CodecConfig)
		if err != nil {
			return nil, err
		}
		if err := trak.SetAVCDescriptor("avc1", [][]byte{sps}, [][]byte{pps}, true); err != nil {
			return nil, merrors.Wrap(merrors.StageAdapter, merrors.Unsupported, 0, err,
				"trak %d avc descriptor", info.ID)
		}
	case CodecAAC:
		if len(info.CodecConfig) == 0 {
			return nil, merrors.New(merrors.StageAdapter, merrors.Malformed,
				"trak %d has no aac config", info.ID)
		}
		// CodecConfig is a raw AudioSpecificConfig; the object type is a
		// 5-bit field, not the first byte.
		asc, err := aac.DecodeAudioSpecificConfig(bytes.NewReader(info.CodecConfig))
		if err != nil {
			return nil, merrors.Wrap(merrors.StageAdapter, merrors.Malformed, 0, err,
				"trak %d aac config", info.ID)
		}
		if err := trak.SetAACDescriptor(asc.ObjectType, int(info.Timescale)); err != nil {
			return nil, merrors.Wrap(merrors.StageAdapter, merrors.Unsupported, 0, err,
				"trak %d aac descriptor", info.ID)
		}
	}

	stbl := trak.Mdia.Minf.Stbl
	// Run-length compress durations.
	stts := &mp4.SttsBox{}
	var keyframes []uint32
	sizes := make([]uint32, len(samples))
	var total uint64
	for i, s := range samples {
		if i == 0 || s.Duration != samples[i-1].Duration || len(stts.SampleCount) == 0 {
			stts.SampleCount = append(stts.SampleCount, 1)
			stts.SampleTimeDelta = append(stts.SampleTimeDelta, uint32(s.Duration))
		} else {
			stts.SampleCount[len(stts.SampleCount)-1]++
		}
		if s.IsKeyframe {
			keyframes = append(keyframes, uint32(i+1))
		}
		sizes[i] = uint32(len(s.Data))
		total += uint64(len(s.Data))
	}
	_ = total

	stbl.Stts = stts
	stbl.Stsc = &mp4.StscBox{
		Entries:             []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(len(samples)), FirstSampleNr: 1}},
		SampleDescriptionID: []uint32{1},
	}
	stbl.Stsz = &mp4.StszBox{SampleNumber: uint32(len(samples)), SampleSize: sizes}
	if mediaType == "video" && len(keyframes) != len(samples) {
		stbl.Stss = &mp4.StssBox{SampleNumber: keyframes}
	}
	stbl.Stco = &mp4.StcoBox{ChunkOffset: []uint32{0}}

	stbl.Children = []mp4.Box{stbl.Stsd, stbl.Stts, stbl.Stsc, stbl.Stsz}
	if stbl.Stss != nil {
		stbl.Children = append(stbl.Children, stbl.Stss)
	}
	stbl.Children = append(stbl.Children, stbl.Stco)
	return trak, nil
}

// avcParams splits a raw avcC decoder config record into SPS and PPS NALUs.
func avcParams(cfg []byte) (sps, pps []byte, err error) {
	rec, err := avc.DecodeAVCDecConfRec(cfg)
	if err != nil {
		return nil, nil, merrors.Wrap(merrors.StageAdapter, merrors.Malformed, 0, err, "avcC config")
	}
	if len(rec.SPSnalus) == 0 || len(rec.PPSnalus) == 0 {
		return nil, nil, merrors.At(merrors.StageAdapter, merrors.Malformed, 0,
			"avcC lacks SPS or PPS")
	}
	return rec.SPSnalus[0], rec.PPSnalus[0], nil
}

func findBoxData(buf []byte, typ string) int {
	off := 0
	for off+8 <= len(buf) {
		size := int(uint32(buf[off])<<24 | uint32(buf[off+1])<<16 | uint32(buf[off+2])<<8 | uint32(buf[off+3]))
		hdr := 8
		if size == 1 {
			if off+16 > len(buf) {
				return -1
			}
			size64 := uint64(buf[off+8])<<56 | uint64(buf[off+9])<<48 | uint64(buf[off+10])<<40 |
				uint64(buf[off+11])<<32 | uint64(buf[off+12])<<24 | uint64(buf[off+13])<<16 |
				uint64(buf[off+14])<<8 | uint64(buf[off+15])
			size = int(size64)
			hdr = 16
		}
		if size < hdr || off+size > len(buf) {
			return -1
		}
		if string(buf[off+4:off+8]) == typ {
			return off + hdr
		}
		off += size
	}
	return -1
}

func findTrak(moov *mp4.MoovBox, id uint32) *mp4.TrakBox {
	for _, t := range moov.Traks {
		if t.Tkhd != nil && t.Tkhd.TrackID == id {
			return t
		}
	}
	return nil
}

// SortTracks orders tracks by ID for deterministic output.
func SortTracks(c *Container) {
	sort.SliceStable(c.Tracks, func(i, j int) bool { return c.Tracks[i].ID < c.Tracks[j].ID })
}
