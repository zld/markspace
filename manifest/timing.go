package manifest

import (
	"bytes"

	"github.com/Eyevinn/mp4ff/mp4"

	"mediacore/index"
	"mediacore/media"
)

// TimingResult is the parsed MP4 sample timing summary used to validate the
// manifest's media timeline.
type TimingResult struct {
	Timescale       uint32
	SampleCount     int
	DurationTicks   int64
	FirstDecodeTime uint64
	LastDecodeTime  uint64
	Complete        bool
}

// MP4Timescale is the timescale used when building the timing track.
const MP4Timescale uint32 = 90000

// BuildTimingSegment converts the indexed frames into a fragmented MP4 media
// segment (one sample per frame) using Eyevinn/mp4ff, so the resulting sample
// timing can be parsed back and compared with the media clock. The segment is
// built entirely in memory; nothing outside the output directory is touched.
func BuildTimingSegment(entries []index.Entry) ([]byte, TimingResult, error) {
	res := TimingResult{Timescale: MP4Timescale, Complete: true}
	initSeg := mp4.CreateEmptyInit()
	initSeg.AddEmptyTrack(MP4Timescale, "video", "und")
	trak := initSeg.Moov.Traks[0]
	width, height := uint16(64), uint16(64)
	if len(entries) > 0 {
		width, height = entries[0].Width, entries[0].Height
	}
	trak.Mdia.Minf.Stbl.Stsd.AddChild(mp4.CreateVisualSampleEntryBox("mp4v", width, height, nil))

	seg := mp4.NewMediaSegment()
	frag, err := mp4.CreateFragment(1, 1)
	if err != nil {
		return nil, res, media.Wrap(media.CatIO, media.StageManifest, err, "create fragment")
	}
	var prevTicks int64
	var prevDur uint32
	first := true
	for i := range entries {
		e := entries[i]
		dur := uint32(0)
		if i > 0 {
			d := e.Ticks - prevTicks
			if d > 0 {
				dur = uint32(d)
			} else {
				dur = prevDur
				res.Complete = false
			}
		}
		if e.DurationTicks > 0 && dur == 0 {
			dur = e.DurationTicks
		}
		if dur == 0 {
			dur = 1
		}
		data := []byte{byte(e.Seq), byte(e.Seq >> 8), byte(e.Seq >> 16), byte(e.Seq >> 24)}
		sample := mp4.NewSample(0x02000000, dur, uint32(len(data)), 0)
		// A single fragment keeps decode times accumulating across samples;
		// separate fragments would each restart at their own base time.
		frag.AddSample(sample, 0)
		frag.Mdat.AddSampleData(data)
		if first {
			res.FirstDecodeTime = 0
			first = false
		}
		res.LastDecodeTime += uint64(dur)
		prevTicks = e.Ticks
		prevDur = dur
	}
	if res.SampleCount > 0 || len(entries) > 0 {
		seg.AddFragment(frag)
	}
	res.SampleCount = len(entries)
	res.DurationTicks = int64(res.LastDecodeTime)

	var out bytes.Buffer
	if err := initSeg.Encode(&out); err != nil {
		return nil, res, media.Wrap(media.CatIO, media.StageManifest, err, "encode init segment")
	}
	if err := seg.Encode(&out); err != nil {
		return nil, res, media.Wrap(media.CatIO, media.StageManifest, err, "encode media segment")
	}
	return out.Bytes(), res, nil
}

// ParseMP4Timing decodes an in-memory fragmented MP4 and returns its sample
// timing, exercising the standard mp4ff sample and time information parser.
func ParseMP4Timing(data []byte) (TimingResult, error) {
	res := TimingResult{Complete: true}
	f, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		return res, media.Wrap(media.CatManifestMismatch, media.StageManifest, err, "decode timing mp4")
	}
	if len(f.Moov.Traks) == 0 {
		return res, media.New(media.CatManifestMismatch, media.StageManifest, "timing mp4 has no track")
	}
	res.Timescale = f.Moov.Traks[0].Mdia.Mdhd.Timescale
	var trex *mp4.TrexBox
	if f.Moov.Mvex != nil && len(f.Moov.Mvex.Trexs) > 0 {
		trex = f.Moov.Mvex.Trexs[0]
	}
	for _, seg := range f.Segments {
		for _, frag := range seg.Fragments {
			samples, err := frag.GetFullSamples(trex)
			if err != nil {
				return res, media.Wrap(media.CatManifestMismatch, media.StageManifest, err, "read mp4 samples")
			}
			for _, s := range samples {
				if res.SampleCount == 0 {
					res.FirstDecodeTime = s.DecodeTime
				}
				res.LastDecodeTime = s.DecodeTime + uint64(s.Dur)
				res.SampleCount++
			}
		}
	}
	if res.LastDecodeTime >= res.FirstDecodeTime {
		res.DurationTicks = int64(res.LastDecodeTime - res.FirstDecodeTime)
	}
	return res, nil
}

// MediaSpanMS returns the media duration covered by the indexed frames,
// including the final frame's nominal duration.
func MediaSpanMS(entries []index.Entry) int64 {
	if len(entries) == 0 {
		return 0
	}
	var dur int64
	for _, e := range entries {
		if e.Ticks+int64(e.DurationTicks) > dur {
			dur = e.Ticks + int64(e.DurationTicks)
		}
	}
	clk := media.MustClock(media.DefaultTimescale)
	return clk.ToMS(dur - entries[0].Ticks)
}

// BindTiming builds and reparses an MP4 segment for the given entries and
// returns the timing binding, including the alignment error against the
// manifest media duration in milliseconds.
func BindTiming(entries []index.Entry, mediaDurationMS int64) (TimingBinding, error) {
	data, built, err := BuildTimingSegment(entries)
	if err != nil {
		return TimingBinding{}, err
	}
	parsed, err := ParseMP4Timing(data)
	if err != nil {
		return TimingBinding{}, err
	}
	if parsed.SampleCount != built.SampleCount {
		return TimingBinding{}, media.New(media.CatManifestMismatch, media.StageManifest,
			"mp4 sample count %d != frame count %d", parsed.SampleCount, built.SampleCount)
	}
	clk := media.MustClock(parsed.Timescale)
	mp4DurMS := clk.ToMS(parsed.DurationTicks)
	align := mp4DurMS - mediaDurationMS
	if align < 0 {
		align = -align
	}
	return TimingBinding{
		Source:             "mp4ff",
		Timescale:          parsed.Timescale,
		SampleCount:        parsed.SampleCount,
		MP4DurationTicks:   parsed.DurationTicks,
		MediaDurationTicks: mediaDurationMS * int64(parsed.Timescale) / 1000,
		AlignmentErrorMS:   align,
		Checked:            true,
	}, nil
}
