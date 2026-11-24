// Package media builds ISO BMFF init and media segments with mp4ff.
//
// Each representation becomes one CMAF-style init segment (ftyp+moov) whose
// tracks carry the normalized audio layout and video color metadata. Each
// aligned boundary becomes one fragmented media segment (styp+moof+mdat) whose
// sample timing comes from the shared media timeline. Sample data is the packet
// bytes the runner already verified, so segment digests cover real output.
package media

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"

	"mediatool/internal/index"
)

// TrackConfig describes one output track.
type TrackConfig struct {
	TrackID    uint32
	Kind       string // "audio" or "video"
	ClockRate  uint32
	Width      int
	Height     int
	Channels   int
	SampleRate int
	Color      *mp4.ColrBox
}

// InitSegment builds a fragmented-file init segment containing one track.
//
// All timestamps in mp4ff are derived from fixed inputs, not wall-clock time,
// so the bytes are reproducible across runs.
func InitSegment(cfg TrackConfig) ([]byte, error) {
	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = 1000
	timescale := cfg.ClockRate
	if cfg.Kind == "audio" && cfg.SampleRate > 0 {
		timescale = uint32(cfg.SampleRate)
	}
	if timescale == 0 {
		timescale = 90000
	}
	init.AddEmptyTrack(timescale, cfg.Kind, "und")
	trak := init.Moov.Traks[0]
	trak.Mdia.Mdhd.Timescale = timescale

	switch cfg.Kind {
	case "video":
		if cfg.Width > 0 {
			trak.Tkhd.Width = mp4.Fixed32(cfg.Width << 16)
			trak.Tkhd.Height = mp4.Fixed32(cfg.Height << 16)
		}
		// A minimal visual sample entry with normalized color. Pass a true nil
		// interface when no color is known so mp4ff omits the colr box.
		var entry *mp4.VisualSampleEntryBox
		if cfg.Color != nil {
			entry = mp4.CreateVisualSampleEntryBox("avc1", uint16(cfg.Width), uint16(cfg.Height), cfg.Color)
		} else {
			entry = mp4.CreateVisualSampleEntryBox("avc1", uint16(cfg.Width), uint16(cfg.Height), nil)
		}
		trak.Mdia.Minf.Stbl.Stsd.AddChild(entry)
	case "audio":
		ch := cfg.Channels
		if ch <= 0 {
			ch = 2
		}
		rate := cfg.SampleRate
		if rate <= 0 {
			rate = int(cfg.ClockRate)
		}
		entry := mp4.CreateAudioSampleEntryBox("mp4a", uint16(ch), 16, uint16(rate), nil)
		trak.Mdia.Minf.Stbl.Stsd.AddChild(entry)
		trak.Tkhd.Volume = 0x0100
	default:
		return nil, fmt.Errorf("media: unsupported track kind %q", cfg.Kind)
	}

	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		return nil, fmt.Errorf("media: encode init: %w", err)
	}
	return buf.Bytes(), nil
}

// Sample is one media sample in the segment.
type Sample struct {
	// DurationTicks is the sample duration in the track clock rate.
	DurationTicks uint32
	// DecodeTimeTicks is the absolute decode time in the track clock rate.
	DecodeTimeTicks uint64
	Data            []byte
	Sync            bool
}

// MediaSegment builds a fragmented media segment for one track.
//
// seq is the fragment sequence number (1-based). Samples must be ordered by
// decode time. The mdat contains exactly the sample bytes, so a digest of the
// segment authenticates the media it carries.
func MediaSegment(trackID uint32, seq uint32, samples []Sample, timescale uint32) ([]byte, error) {
	seg := mp4.NewMediaSegment()
	frag, err := mp4.CreateFragment(seq, trackID)
	if err != nil {
		return nil, fmt.Errorf("media: create fragment: %w", err)
	}
	if len(samples) > 0 {
		frag.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(samples[0].DecodeTimeTicks)
	}
	for _, s := range samples {
		flags := mp4.NonSyncSampleFlags
		if s.Sync {
			flags = mp4.SyncSampleFlags
		}
		sample := mp4.Sample{Dur: s.DurationTicks, Size: uint32(len(s.Data)), Flags: flags}
		frag.AddSample(sample, s.DecodeTimeTicks)
		frag.Mdat.AddSampleData(s.Data)
	}
	seg.AddFragment(frag)

	var buf bytes.Buffer
	if err := seg.Encode(&buf); err != nil {
		return nil, fmt.Errorf("media: encode segment: %w", err)
	}
	return buf.Bytes(), nil
}

// ColorBox converts normalized index color metadata into an mp4 colr box.
// Unknown color yields nil so the sample entry omits the box.
func ColorBox(vi *index.VideoInfo) *mp4.ColrBox {
	if vi == nil || !vi.ColorKnown {
		return nil
	}
	// The index records the color as "p<prim>/t<transfer>/m<matrix>/full=<bool>".
	var p, t, m uint16
	var full bool
	if _, err := fmt.Sscanf(vi.Color, "p%d/t%d/m%d/full=%t", &p, &t, &m, &full); err != nil {
		return nil
	}
	return &mp4.ColrBox{
		ColorType:               "nclx",
		ColorPrimaries:          p,
		TransferCharacteristics: t,
		MatrixCoefficients:      m,
		FullRangeFlag:           full,
	}
}
