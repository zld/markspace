// Package meta validates channel layouts and cross-source media metadata.
// Mismatches are explicit structured errors carrying a file offset; the
// kernel never silently remaps channels, resamples or converts color.
package meta

import (
	"fmt"

	"mediakernel/internal/clock"
	"mediakernel/internal/wav"
)

// Layout describes one expected multi-channel arrangement plus the sample
// time base and optional color metadata associated with a recording.
type Layout struct {
	Name        string
	Channels    uint16
	ChannelTags []string
	SampleRate  uint32
	// ColorProfile is descriptive only (e.g. "sRGB"); no conversion is done.
	ColorProfile string
}

// StandardLayouts provides the layouts used by regression fixtures.
func StandardLayouts(channels uint16, rate uint32) (Layout, error) {
	var tags []string
	switch channels {
	case 1:
		tags = []string{"mono"}
	case 2:
		tags = []string{"L", "R"}
	case 4:
		tags = []string{"L", "R", "BL", "BR"}
	case 6:
		tags = []string{"L", "R", "C", "LFE", "BL", "BR"}
	case 8:
		tags = []string{"L", "R", "C", "LFE", "BL", "BR", "SL", "SR"}
	default:
		return Layout{}, clock.NewError("meta", clock.CatLayoutMismatch,
			"no standard layout for %d channels", channels)
	}
	return Layout{
		Name:         fmt.Sprintf("%dch-%dHz", channels, rate),
		Channels:     channels,
		ChannelTags:  tags,
		SampleRate:   rate,
		ColorProfile: "sRGB",
	}, nil
}

// ValidateLayout checks internal consistency: tag count must match channel
// count and the rate must be positive.
func (l Layout) ValidateLayout() error {
	if l.Channels == 0 {
		return clock.NewError("meta", clock.CatInvalidParam,
			"layout %q has zero channels", l.Name)
	}
	if len(l.ChannelTags) != int(l.Channels) {
		return clock.NewError("meta", clock.CatLayoutMismatch,
			"layout %q declares %d channels but %d channel tags",
			l.Name, l.Channels, len(l.ChannelTags))
	}
	if l.SampleRate == 0 {
		return clock.NewError("meta", clock.CatInvalidParam,
			"layout %q has zero sample rate", l.Name)
	}
	return nil
}

// CheckWAV verifies that an opened WAV file matches the expected layout and
// sample time base. Any mismatch is rejected (not remixed/resampled) and
// located at the fmt chunk payload offset inside the file.
func CheckWAV(l Layout, h wav.Header) error {
	if err := l.ValidateLayout(); err != nil {
		return err
	}
	fmtOff := int64(20) // fmt payload starts at byte 20 in a canonical WAV
	if h.Channels != l.Channels {
		return clock.NewError("meta", clock.CatLayoutMismatch,
			"channel count conflict: layout=%d wav=%d", l.Channels, h.Channels).At(fmtOff + 2)
	}
	if h.SampleRate != l.SampleRate {
		return clock.NewError("meta", clock.CatLayoutMismatch,
			"sample rate conflict: layout=%d wav=%d", l.SampleRate, h.SampleRate).At(fmtOff + 4)
	}
	if h.BitsPerSample == 0 || h.BitsPerSample%8 != 0 {
		return clock.NewError("meta", clock.CatBadLength,
			"invalid wav bit depth %d", h.BitsPerSample).At(fmtOff + 14)
	}
	return nil
}

// CheckRTPRate verifies that the RTP clock rate governing an SSRC matches
// the layout sample rate, so RTP and PCM share one sample time base.
func CheckRTPRate(l Layout, ssrc uint32, rtpClockRate uint32) error {
	if err := l.ValidateLayout(); err != nil {
		return err
	}
	if rtpClockRate != l.SampleRate {
		return clock.NewError("meta", clock.CatLayoutMismatch,
			"rtp clock rate %d for ssrc=%d != layout sample rate %d",
			rtpClockRate, ssrc, l.SampleRate)
	}
	return nil
}

// RejectRange refuses interval reads against a mismatched layout, returning
// the same structured error shape with the recorded file offset.
func RejectRange(mismatch error, rangeStart int64) error {
	ce, ok := clock.AsError(mismatch)
	if !ok {
		return clock.NewError("meta", clock.CatLayoutMismatch,
			"range [%d,...] rejected: %v", rangeStart, mismatch)
	}
	return clock.NewError("meta", clock.CatLayoutMismatch,
		"range starting at frame %d rejected due to earlier layout mismatch",
		rangeStart).At(ce.ByteOffset).Wrap(mismatch)
}
