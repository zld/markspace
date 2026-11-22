package ingest

import (
	"bytes"
	"io"
	"sort"

	"github.com/abema/go-mp4"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

// MP4Options configures the MP4 reader.
type MP4Options struct {
	Track model.TrackID
	// Kind selects which trak to read when the file has several.
	Kind model.MediaKind
	// Modulus is 0: MP4 sample times are signed 64-bit and do not wrap.
	Modulus int64
	// JitterTicks tolerates small out-of-order edit offsets.
	JitterTicks int64
}

// MP4Track is the recovered view of one MP4 track.
type MP4Track struct {
	Track        model.TrackID
	Timescale    uint32
	Codec        string
	HandlerType  string
	Width        uint16
	Height       uint16
	ChannelCount uint16
	SampleRate   uint32
	Color        *model.ColorDeclaration
	AudioDesc    model.AudioDescriptor
	EditList     []clock.Decision
	Samples      []model.Sample
	// SampleOffsets are the byte offsets of each sample inside Data.
	SampleOffsets []uint64
	Data          []byte
}

// rawTrack is the box-level view of one trak before sample-table assembly.
type rawTrack struct {
	trak      *mp4.BoxInfo
	handler   string
	timescale uint32
	codec     string
	avc1      *mp4.VisualSampleEntry
	mp4a      *mp4.AudioSampleEntry
	color     *model.ColorDeclaration
	elst      *mp4.Elst
	stts      *mp4.Stts
	stsc      *mp4.Stsc
	stsz      *mp4.Stsz
	stco      *mp4.Stco
	co64      *mp4.Co64
	stss      *mp4.Stss
}

// ReadMP4 recovers one track from a complete in-memory MP4 file. Exact
// rational time is preserved: sample times are converted from the media
// timescale without float accumulation, and edit-list offsets become repair
// decisions rather than silent shifts.
func ReadMP4(data []byte, opts MP4Options) (MP4Track, error) {
	if opts.Track == "" {
		opts.Track = model.TrackVideoMain
	}
	var out MP4Track
	out.Track = opts.Track
	r := bytes.NewReader(data)

	traks, err := mp4.ExtractBoxes(r, nil, []mp4.BoxPath{{mp4.BoxTypeMoov(), mp4.BoxTypeTrak()}})
	if err != nil {
		return out, model.WrapError(opts.Track, model.StageParse, model.CodeMalformedMP4, false, err,
			"MP4 trak extraction failed")
	}
	if len(traks) == 0 {
		return out, model.NewError(opts.Track, model.StageParse, model.CodeMalformedMP4, false,
			"MP4 contains no trak box")
	}

	var chosen *rawTrack
	for _, bi := range traks {
		rt, err := readRawTrack(r, bi)
		if err != nil {
			return out, model.WrapError(opts.Track, model.StageParse, model.CodeMalformedMP4, false, err,
				"MP4 trak could not be parsed")
		}
		if chosen == nil {
			chosen = rt
		}
		want := handlerForKind(opts.Kind)
		if want != "" && rt.handler == want {
			chosen = rt
			break
		}
	}
	return assembleTrack(r, data, chosen, opts)
}

func handlerForKind(k model.MediaKind) string {
	switch k {
	case model.KindVideo:
		return "vide"
	case model.KindAudio:
		return "soun"
	case model.KindText:
		return "sbtl"
	case model.KindChapter:
		return "text"
	default:
		return ""
	}
}

func readRawTrack(r io.ReadSeeker, trak *mp4.BoxInfo) (*rawTrack, error) {
	rt := &rawTrack{trak: trak}
	bips, err := mp4.ExtractBoxesWithPayload(r, trak, []mp4.BoxPath{
		{mp4.BoxTypeTkhd()},
		{mp4.BoxTypeEdts(), mp4.BoxTypeElst()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAvc1()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAvc1(), mp4.BoxTypeColr()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeMp4a()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStts()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsc()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsz()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStco()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeCo64()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStss()},
	})
	if err != nil {
		return nil, err
	}
	for _, bip := range bips {
		switch p := bip.Payload.(type) {
		case *mp4.Elst:
			rt.elst = p
		case *mp4.Mdhd:
			rt.timescale = p.Timescale
		case *mp4.Hdlr:
			rt.handler = string(p.HandlerType[:])
		case *mp4.VisualSampleEntry:
			rt.avc1 = p
			rt.codec = "avc1"
		case *mp4.Colr:
			rt.color = colorFromColr(p)
		case *mp4.AudioSampleEntry:
			rt.mp4a = p
			rt.codec = "mp4a"
		case *mp4.Stts:
			rt.stts = p
		case *mp4.Stsc:
			rt.stsc = p
		case *mp4.Stsz:
			rt.stsz = p
		case *mp4.Stco:
			rt.stco = p
		case *mp4.Co64:
			rt.co64 = p
		case *mp4.Stss:
			rt.stss = p
		}
	}
	if rt.timescale == 0 {
		return nil, model.NewError("", model.StageParse, model.CodeMalformedMP4, false,
			"trak has no mdhd timescale")
	}
	if rt.stts == nil || rt.stsc == nil || (rt.stco == nil && rt.co64 == nil) {
		return nil, model.NewError("", model.StageParse, model.CodeMalformedMP4, false,
			"trak %s lacks stts/stsc/stco", rt.handler)
	}
	return rt, nil
}

func colorFromColr(colr *mp4.Colr) *model.ColorDeclaration {
	if string(colr.ColourType[:]) != "nclx" && string(colr.ColourType[:]) != "nclc" {
		return nil
	}
	return &model.ColorDeclaration{
		Source:    model.ColorFromContainer,
		Primaries: colr.ColourPrimaries,
		Transfer:  colr.TransferCharacteristics,
		Matrix:    colr.MatrixCoefficients,
		FullRange: colr.FullRangeFlag,
	}
}

// assembleTrack converts the box-level sample table into repaired samples.
func assembleTrack(r io.ReadSeeker, data []byte, rt *rawTrack, opts MP4Options) (MP4Track, error) {
	var out MP4Track
	out.Track = opts.Track
	if rt == nil {
		return out, model.NewError(opts.Track, model.StageParse, model.CodeMalformedMP4, false,
			"no matching trak")
	}
	out.Timescale = rt.timescale
	out.Codec = rt.codec
	out.HandlerType = rt.handler
	out.Color = rt.color
	if rt.avc1 != nil {
		out.Width, out.Height = rt.avc1.Width, rt.avc1.Height
	}
	if rt.mp4a != nil {
		out.ChannelCount = rt.mp4a.ChannelCount
		out.SampleRate = rt.mp4a.SampleRate >> 16
		out.AudioDesc = model.AudioDescriptor{
			Source:       "mp4-stsd",
			ChannelCount: int(rt.mp4a.ChannelCount),
			SampleRate:   rt.mp4a.SampleRate >> 16,
		}
	}

	repairer, err := clock.NewRepairer(opts.Track, clock.RepairOptions{
		Timescale: rt.timescale, Modulus: opts.Modulus, JitterTicks: opts.JitterTicks,
	})
	if err != nil {
		return out, err
	}
	if rt.elst != nil {
		for i := range rt.elst.Entries {
			mediaTime := rt.elst.GetMediaTime(i)
			out.EditList = append(out.EditList, clock.Decision{
				Track: opts.Track, Kind: clock.DecisionOrigin, RawTicks: mediaTime, Recoverable: true,
				Message: "edit list entry " + itoa(i) + " media_time=" + i64s(mediaTime),
			})
			if mediaTime > 0 {
				repairer.SetAnchor(mediaTime)
			}
		}
	}

	sizes := sampleSizes(rt.stsz)
	offsets, err := sampleOffsets(rt, sizes, data)
	if err != nil {
		return out, model.WrapError(opts.Track, model.StageParse, model.CodeMalformedMP4, false, err, "sample offsets")
	}
	syncSet := syncSamples(rt.stss, len(sizes))
	mdatBase := uint64(0)
	if m := findMdat(data); m != nil {
		mdatBase = m.offset
	}
	out.Data = append([]byte(nil), data[mdatBase:]...)
	out.SampleOffsets = make([]uint64, len(offsets))
	for i, off := range offsets {
		if off < mdatBase || off > uint64(len(data)) {
			return out, model.NewError(opts.Track, model.StageParse, model.CodeMalformedMP4, false,
				"sample %d chunk offset %d outside mdat", i, off)
		}
		out.SampleOffsets[i] = off - mdatBase
	}

	tick := int64(0)
	di := 0
	for i, size := range sizes {
		delta := uint32(0)
		for di < len(rt.stts.Entries) {
			e := rt.stts.Entries[di]
			if e.SampleCount == 0 {
				di++
				continue
			}
			delta = e.SampleDelta
			rt.stts.Entries[di].SampleCount--
			break
		}
		ts, err := repairer.Decode(tick)
		if err != nil {
			return out, model.WrapError(opts.Track, model.StageMetadata, model.CodeBackwardTimestamp, false, err,
				"MP4 sample %d", i)
		}
		dur, err := clock.FromTicks(int64(delta), rt.timescale)
		if err != nil {
			return out, err
		}
		out.Samples = append(out.Samples, model.Sample{
			Index: i, Track: opts.Track, Time: ts,
			Duration: clock.ToModel(dur), Size: size, Sync: syncSet[i],
		})
		tick += int64(delta)
	}
	out.EditList = append(out.EditList, repairer.Decisions()...)
	return out, nil
}

func sampleSizes(stsz *mp4.Stsz) []uint32 {
	n := int(stsz.SampleCount)
	sizes := make([]uint32, 0, n)
	if stsz.SampleSize != 0 {
		for i := 0; i < n; i++ {
			sizes = append(sizes, stsz.SampleSize)
		}
		return sizes
	}
	return append(sizes, stsz.EntrySize...)
}

func chunkOffsets(rt *rawTrack) []uint64 {
	if rt.stco != nil {
		out := make([]uint64, len(rt.stco.ChunkOffset))
		for i, v := range rt.stco.ChunkOffset {
			out[i] = uint64(v)
		}
		return out
	}
	return append([]uint64(nil), rt.co64.ChunkOffset...)
}

func sampleOffsets(rt *rawTrack, sizes []uint32, data []byte) ([]uint64, error) {
	chunks := chunkOffsets(rt)
	offsets := make([]uint64, 0, len(sizes))
	sample := 0
	for ci := range chunks {
		perChunk := samplesPerChunk(rt.stsc, ci, len(chunks))
		pos := chunks[ci]
		for k := uint32(0); k < perChunk && sample < len(sizes); k++ {
			offsets = append(offsets, pos)
			pos += uint64(sizes[sample])
			sample++
		}
	}
	if sample != len(sizes) {
		return nil, mperr("stsc describes %d samples but stsz has %d", sample, len(sizes))
	}
	for i := 1; i < len(offsets); i++ {
		if offsets[i] < offsets[i-1] {
			return nil, mperr("chunk offsets are not monotonic at sample %d", i)
		}
	}
	return offsets, nil
}

func samplesPerChunk(stsc *mp4.Stsc, chunkIndex, chunkCount int) uint32 {
	first := uint32(1)
	for _, e := range stsc.Entries {
		if e.FirstChunk >= 1 && int(e.FirstChunk-1) <= chunkIndex {
			first = e.SamplesPerChunk
		} else {
			break
		}
	}
	if chunkIndex >= chunkCount {
		return 0
	}
	return first
}

func syncSamples(stss *mp4.Stss, total int) map[int]bool {
	set := make(map[int]bool, total)
	if stss == nil {
		for i := 0; i < total; i++ {
			set[i] = true
		}
		return set
	}
	for _, n := range stss.SampleNumber {
		if n >= 1 && int(n-1) < total {
			set[int(n-1)] = true
		}
	}
	return set
}

// SamplePayload returns the bytes of sample i.
func (t MP4Track) SamplePayload(i int) ([]byte, bool) {
	if i < 0 || i >= len(t.SampleOffsets) || i >= len(t.Samples) {
		return nil, false
	}
	start := t.SampleOffsets[i]
	end := start + uint64(t.Samples[i].Size)
	if end > uint64(len(t.Data)) {
		return nil, false
	}
	return t.Data[start:end], true
}

// SortSamplesByTime orders samples deterministically by repaired time.
func SortSamplesByTime(samples []model.Sample) {
	sort.SliceStable(samples, func(i, j int) bool {
		a, _ := clock.New(samples[i].Time.RepairedTime.Num, samples[i].Time.RepairedTime.Den)
		b, _ := clock.New(samples[j].Time.RepairedTime.Num, samples[j].Time.RepairedTime.Den)
		if c := a.Cmp(b); c != 0 {
			return c < 0
		}
		return samples[i].Index < samples[j].Index
	})
}
