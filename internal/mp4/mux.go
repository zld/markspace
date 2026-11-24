// Package mp4mux repackages validated frame sequences into ISO-BMFF files
// using go-mp4.
//
// Every input record becomes exactly one video sample, in the original order,
// carrying its presentation and decode times rebucketed to the output
// timescale. Recovered audio units become a second track; concealment slots
// keep their duration but contribute no bytes. The mapping from MP4 sample back
// to the ingest bytes is emitted alongside the segment by the provenance
// manifest, which is why this package also reports the resolved keyframe index.
package mp4mux

import (
	"fmt"
	"io"
	"sort"

	"github.com/abema/go-mp4"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
	"mediarun/internal/frames"
	"mediarun/internal/index"
	"mediarun/internal/syncer"
)

// Config controls the remux output.
type Config struct {
	Timescale      uint32 // movie and video sample timescale, default 90000
	AudioTimescale uint32 // audio sample timescale, default 48000
	Width          int
	Height         int
	MaxSamples     int // hard cap on video samples per segment
}

func (c *Config) defaults() error {
	if c.Timescale == 0 {
		c.Timescale = 90000
	}
	if c.AudioTimescale == 0 {
		c.AudioTimescale = 48000
	}
	if c.Width == 0 {
		c.Width = 1280
	}
	if c.Height == 0 {
		c.Height = 720
	}
	if c.MaxSamples == 0 {
		c.MaxSamples = 100000
	}
	if c.Width <= 0 || c.Height <= 0 || c.Width > 16384 || c.Height > 16384 {
		return fmt.Errorf("dimensions %dx%d outside 1..16384", c.Width, c.Height)
	}
	if c.Timescale == 0 || c.Timescale > 1_000_000 || c.AudioTimescale > 1_000_000 {
		return fmt.Errorf("timescales %d/%d outside 1..1000000", c.Timescale, c.AudioTimescale)
	}
	if c.MaxSamples <= 0 {
		return fmt.Errorf("max samples must be positive")
	}
	return nil
}

// Sample describes one MP4 video sample.
type Sample struct {
	Record *frames.Record
	// Payload overrides Record.Payload when non-nil, letting the pipeline
	// stream metadata and re-read payloads at write time.
	Payload    []byte
	Duration   uint32
	PTS        uint64 // rebucketed to the output timescale
	DTS        uint64
	Keyframe   bool
	SourceOff  int64
	SourceHash [32]byte
}

// AudioSample is one encoded audio frame or a timed concealment slot.
type AudioSample struct {
	Data     []byte
	Duration uint32
	PTS      uint64
	Silence  bool
}

// Result reports what was written.
type Result struct {
	Bytes         int64
	Samples       int
	AudioFrames   int
	AudioBytes    int
	VideoChunkOff int64
	AudioChunkOff int64
	VideoTrack    uint32
	AudioTrack    uint32
	Keyframes     []index.Entry
	PTSList       []uint64
	SizeList      []uint32
	// SampleOffsets holds the absolute file offset of every video sample.
	SampleOffsets []int64
}

// Mux writes an MP4 to w. Sample order is never changed: file sample i is
// input record i. Timestamps are rebucketed only to fit the output timescale.
func Mux(w io.WriteSeeker, alignment *syncer.Report, tl *clock.Timeline, cfg Config) (*Result, error) {
	if alignment == nil || tl == nil {
		return nil, fault.New(fault.StageMux, fault.KindConfig, "mux needs an alignment report and a timeline")
	}
	if err := cfg.defaults(); err != nil {
		return nil, fault.New(fault.StageMux, fault.KindConfig, "%v", err)
	}
	if len(alignment.Video) == 0 {
		return nil, fault.New(fault.StageMux, fault.KindIncomplete, "no video samples to mux")
	}
	if len(alignment.Video) > cfg.MaxSamples {
		return nil, fault.New(fault.StageMux, fault.KindLimit,
			"sample count %d exceeds cap %d", len(alignment.Video), cfg.MaxSamples)
	}
	samples, err := buildSamples(alignment, tl, cfg)
	if err != nil {
		return nil, err
	}
	audio, err := buildAudio(alignment, tl, cfg)
	if err != nil {
		return nil, err
	}
	return writeFile(w, samples, audio, cfg, alignment)
}

// buildSamples rebuckets local ticks and derives each duration from the next
// PTS. The final sample reuses the previous duration, which keeps total
// duration equal to the frame span rather than inventing a tail.
func buildSamples(rep *syncer.Report, tl *clock.Timeline, cfg Config) ([]Sample, error) {
	first := rep.Video[0].LocalTick
	out := make([]Sample, len(rep.Video))
	for i, vf := range rep.Video {
		local := vf.LocalTick - first
		if local < 0 {
			return nil, fault.New(fault.StageMux, fault.KindTimestamp,
				"sample %d local tick %d is negative", i, local)
		}
		out[i] = Sample{
			Record:     vf.Record,
			PTS:        uint64(tl.Rebucket(local, cfg.Timescale)),
			DTS:        uint64(tl.Rebucket(local, cfg.Timescale)),
			Keyframe:   vf.Record.Keyframe(),
			SourceOff:  vf.Record.Offset,
			SourceHash: vf.Record.Meta.Checksum,
		}
	}
	defaultDur := uint32(clock.ScaleTicks(3000, 90000, cfg.Timescale))
	if defaultDur == 0 {
		defaultDur = 1
	}
	for i := range out {
		if i+1 < len(out) {
			d := out[i+1].PTS - out[i].PTS
			if d == 0 || d > uint64(^uint32(0)) {
				d = uint64(defaultDur)
			}
			out[i].Duration = uint32(d)
			continue
		}
		if i > 0 {
			out[i].Duration = out[i-1].Duration
		} else {
			out[i].Duration = defaultDur
		}
	}
	return out, nil
}

// buildAudio converts recovered units into encoded audio samples. Empty
// concealment slots still consume timeline duration through stts.
func buildAudio(rep *syncer.Report, tl *clock.Timeline, cfg Config) ([]AudioSample, error) {
	if len(rep.Audio) == 0 {
		return nil, nil
	}
	first := rep.Audio[0].LocalTick
	out := make([]AudioSample, 0, len(rep.Audio))
	for i, slot := range rep.Audio {
		local := slot.LocalTick - first
		if local < 0 {
			return nil, fault.New(fault.StageMux, fault.KindTimestamp,
				"audio slot %d local tick %d is negative", i, local)
		}
		var data []byte
		if slot.Unit.Packet != nil {
			data = slot.Unit.Packet.Payload
		}
		dur := uint32(clock.ScaleTicks(slot.DurationTicks, tl.Timescale, cfg.AudioTimescale))
		if dur == 0 {
			dur = cfg.AudioTimescale / 50
			if dur == 0 {
				dur = 1
			}
		}
		out = append(out, AudioSample{
			Data:     data,
			Duration: dur,
			PTS:      uint64(clock.ScaleTicks(local, tl.Timescale, cfg.AudioTimescale)),
			Silence:  slot.Unit.Silence,
		})
	}
	return out, nil
}

// writeFile lays out ftyp, mdat then moov. Keeping mdat first means chunk
// offsets are known before any sample table is written, so no back-patching of
// stco is needed and a partially written file never contains a valid but
// shifted index.
func writeFile(w io.WriteSeeker, samples []Sample, audio []AudioSample, cfg Config, rep *syncer.Report) (*Result, error) {
	mw := mp4.NewWriter(w)

	ftyp := &mp4.Ftyp{MajorBrand: mp4.BrandISO6()}
	ftyp.AddCompatibleBrand(mp4.BrandISOM())
	ftyp.AddCompatibleBrand(mp4.BrandISO6())
	ftyp.AddCompatibleBrand(mp4.BrandAVC1())
	if err := writeBox(mw, ftyp, mp4.Context{}); err != nil {
		return nil, wrapMux("ftyp", err)
	}

	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, wrapMux("mdat header", err)
	}
	videoStart, err := mw.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, wrapMux("mdat position", err)
	}
	var videoBytes, audioBytes int64
	for i := range samples {
		n, err := mw.Write(samples[i].payload())
		if err != nil {
			return nil, wrapMux("video payload", err)
		}
		videoBytes += int64(n)
	}
	audioStart := videoStart + videoBytes
	for _, a := range audio {
		if len(a.Data) == 0 {
			continue
		}
		n, err := mw.Write(a.Data)
		if err != nil {
			return nil, wrapMux("audio payload", err)
		}
		audioBytes += int64(n)
	}
	if _, err := mw.EndBox(); err != nil {
		return nil, wrapMux("mdat size", err)
	}

	videoDur := uint64(0)
	for _, s := range samples {
		videoDur += uint64(s.Duration)
	}
	audioDur := uint64(0)
	for _, a := range audio {
		audioDur += uint64(a.Duration)
	}
	movieDur := videoDur
	if audioDur > 0 {
		audioMovie := audioDur * uint64(cfg.Timescale) / uint64(cfg.AudioTimescale)
		if audioMovie > movieDur {
			movieDur = audioMovie
		}
	}

	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, wrapMux("moov header", err)
	}
	mvhd := &mp4.Mvhd{
		Timescale:   cfg.Timescale,
		DurationV0:  uint32(movieDur),
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      identityMatrix(),
		NextTrackID: 3,
	}
	if err := writeBox(mw, mvhd, mp4.Context{}); err != nil {
		return nil, wrapMux("mvhd", err)
	}
	if err := writeVideoTrak(mw, samples, videoDur, cfg, videoStart); err != nil {
		return nil, wrapMux("video trak", err)
	}
	if len(audio) > 0 {
		if err := writeAudioTrak(mw, audio, audioDur, cfg, audioStart); err != nil {
			return nil, wrapMux("audio trak", err)
		}
	}
	if _, err := mw.EndBox(); err != nil {
		return nil, wrapMux("moov size", err)
	}
	end, err := mw.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, wrapMux("file position", err)
	}

	keyframes := keyframeIndex(samples, videoStart)
	return &Result{
		Bytes:         end,
		Samples:       len(samples),
		AudioFrames:   len(audio),
		AudioBytes:    int(audioBytes),
		VideoChunkOff: videoStart,
		AudioChunkOff: audioStart,
		VideoTrack:    1,
		AudioTrack:    2,
		Keyframes:     keyframes,
		PTSList:       ptsList(samples),
		SizeList:      sizeList(samples),
		SampleOffsets: sampleOffsets(samples, videoStart),
	}, nil
}

// payload returns the bytes to write for this sample.
func (s Sample) payload() []byte {
	if s.Payload != nil {
		return s.Payload
	}
	return s.Record.Payload
}

func ptsList(samples []Sample) []uint64 {
	out := make([]uint64, len(samples))
	for i, s := range samples {
		out[i] = s.PTS
	}
	return out
}

func sampleOffsets(samples []Sample, chunkStart int64) []int64 {
	out := make([]int64, len(samples))
	off := chunkStart
	for i, s := range samples {
		out[i] = off
		off += int64(len(s.payload()))
	}
	return out
}

func sizeList(samples []Sample) []uint32 {
	out := make([]uint32, len(samples))
	for i, s := range samples {
		out[i] = uint32(len(s.payload()))
	}
	return out
}

// keyframeIndex resolves each keyframe's absolute payload offset and digest.
func keyframeIndex(samples []Sample, chunkStart int64) []index.Entry {
	offsets := make([]int64, len(samples))
	off := chunkStart
	for i, s := range samples {
		offsets[i] = off
		off += int64(len(s.payload()))
	}
	out := make([]index.Entry, 0, len(samples)/4+1)
	for i, s := range samples {
		if !s.Keyframe {
			continue
		}
		out = append(out, index.Entry{
			Sample:      i + 1,
			Track:       1,
			PTS:         s.PTS,
			SourceOff:   s.SourceOff,
			SourceHash:  s.SourceHash,
			PayloadHash: index.PayloadHash(s.payload()),
			DataOffset:  offsets[i],
			Size:        len(s.payload()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sample < out[j].Sample })
	return out
}

func wrapMux(what string, err error) error {
	return fault.Wrap(fault.StageMux, fault.KindIO, err, "write %s", what)
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

// writeBox marshals a leaf box and closes its size.
func writeBox(w *mp4.Writer, box mp4.IImmutableBox, ctx mp4.Context) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: box.GetType()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, box, ctx); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}
