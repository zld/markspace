// Package codec parses variable-duration Opus and AAC audio packets.
package codec

import (
	"time"

	"example.com/media-kernel/media"
)

// Type identifies a supported audio coding format.
type Type string

const (
	TypeOpus Type = "opus"
	TypeAAC  Type = "aac"
)

// Info is the duration-relevant information extracted from one packet.
type Info struct {
	Codec        Type   `json:"codec"`
	Samples      int64  `json:"samples"`
	Frames       int    `json:"frames"`
	SampleRateHz uint32 `json:"sample_rate_hz"`
	Channels     uint8  `json:"channels,omitempty"`
	Profile      string `json:"profile,omitempty"`
}

// Timebase returns the clock in which Samples and RTP timestamps are expressed.
func (i Info) Timebase() media.Timebase {
	return media.Timebase{Numerator: 1, Denominator: int64(i.SampleRateHz)}
}

// Duration converts the parsed sample count to a time.Duration.
func (i Info) Duration() time.Duration {
	return i.Timebase().Duration(i.Samples)
}

// RangeFrom builds and validates the packet's half-open media time range.
func (i Info) RangeFrom(startTicks int64) (media.TimeRange, error) {
	return media.NewRange(i.Timebase(), startTicks, i.Samples)
}

// Analyzed is a packet together with its successfully parsed information.
type Analyzed struct {
	ExtSequence  uint64           `json:"ext_sequence"`
	RTPTimestamp uint32           `json:"rtp_timestamp"`
	Info         *Info            `json:"info,omitempty"`
	Range        *media.TimeRange `json:"range,omitempty"`
	Error        *media.Error     `json:"error,omitempty"`
}

// Analyze parses one packet and annotates parse errors with sequence and a
// point-in-time range. It never replaces an invalid timestamp by zero.
func Analyze(codec Type, extSequence uint64, rtpTimestamp uint32, payload []byte) Analyzed {
	out := Analyzed{ExtSequence: extSequence, RTPTimestamp: rtpTimestamp}
	var (
		info *Info
		err  *media.Error
	)
	switch codec {
	case TypeOpus:
		info, err = ParseOpus(payload)
	case TypeAAC:
		info, err = ParseAAC(payload)
	default:
		err = media.NewError("codec", media.CodeUnsupported, "unsupported codec "+string(codec), nil)
	}
	if err != nil {
		start := int64(rtpTimestamp)
		tb := media.Timebase48k
		if codec == TypeAAC {
			// ADTS may still reveal its rate even when a later frame is truncated.
			if partial, _ := ParseADTSHeader(payload); partial != nil {
				tb = partial.Timebase()
			}
		}
		r, rangeErr := media.NewRange(tb, start, 0)
		if rangeErr != nil {
			err = rangeErr.(*media.Error)
		} else {
			err.WithSeq(extSequence).WithRange(&r)
		}
		out.Error = err
		return out
	}

	r, rangeErr := info.RangeFrom(int64(rtpTimestamp))
	if rangeErr != nil {
		e := rangeErr.(*media.Error).WithSeq(extSequence)
		out.Error = e
		return out
	}
	out.Info = info
	out.Range = &r
	return out
}
