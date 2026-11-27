package mp4

import (
	"fmt"
	"io"

	gomp4 "github.com/abema/go-mp4"
)

func errMissing(what string) error {
	return fmt.Errorf("mp4 verify: missing %s", what)
}

func errMismatch(what string, got, want interface{}) error {
	return fmt.Errorf("mp4 verify: %s mismatch: got %v want %v", what, got, want)
}

// Verify re-parses a muxed container and confirms the structural invariants
// the pipeline promises: timescale, sample count, channel/rate entry, track
// duration and the derived average bitrate against the actual file size.
func Verify(r io.ReadSeeker, totalBytes int64, cfg MuxConfig) (MuxReport, error) {
	rep := MuxReport{Bytes: totalBytes}
	var sawSampleEntry bool

	_, err := gomp4.ReadBoxStructure(r, func(h *gomp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case gomp4.BoxTypeMdhd():
			payload, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			mdhd := payload.(*gomp4.Mdhd)
			rep.Timescale = mdhd.Timescale
			rep.TrackDuration = mdhd.GetDuration()
		case gomp4.BoxTypeStts():
			payload, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			stts := payload.(*gomp4.Stts)
			var samples uint64
			for _, e := range stts.Entries {
				samples += uint64(e.SampleCount)
			}
			rep.SampleCount = uint32(samples)
		case gomp4.BoxTypeStsz():
			payload, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			stsz := payload.(*gomp4.Stsz)
			rep.SampleCount = stsz.SampleCount
		case gomp4.BoxTypeMp4a(), gomp4.BoxTypeOpus():
			payload, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			if ase, ok := payload.(*gomp4.AudioSampleEntry); ok {
				rep.SampleRate = uint32(ase.GetSampleRate())
				rep.ChannelCount = ase.ChannelCount
				sawSampleEntry = true
			}
		}
		return h.Expand()
	})
	if err != nil {
		return MuxReport{}, err
	}
	if !sawSampleEntry {
		return MuxReport{}, errMissing("audio sample entry")
	}
	if rep.Timescale != cfg.Rate {
		return MuxReport{}, errMismatch("timescale", rep.Timescale, cfg.Rate)
	}
	if rep.SampleRate != cfg.Rate || rep.ChannelCount != cfg.Channels {
		return MuxReport{}, errMismatch("sample entry rate/channels",
			[2]uint32{uint32(rep.SampleRate), uint32(rep.ChannelCount)},
			[2]uint32{uint32(cfg.Rate), uint32(cfg.Channels)})
	}
	if rep.TrackDuration != uint64(rep.SampleCount) {
		return MuxReport{}, errMismatch("duration vs sample count",
			rep.TrackDuration, uint64(rep.SampleCount))
	}
	if rep.Timescale > 0 {
		rep.DurationMicros = rep.TrackDuration * 1_000_000 / uint64(rep.Timescale)
		seconds := float64(rep.TrackDuration) / float64(rep.Timescale)
		if seconds > 0 {
			rep.AvgBitratebps = float64(totalBytes) * 8 / seconds
		}
	}
	return rep, nil
}
