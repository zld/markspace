// Package pipeline wires the parse, transform and validate stages of
// the offline RTP/WAV regression flow. Inputs are never modified and
// every stage keeps its own structured anomalies.
package pipeline

import (
	"bytes"

	"github.com/abema/go-mp4"

	"rtx/internal/model"
	"rtx/internal/rtp"
	"rtx/internal/wav"
)

// Input is the parsed, stage-1 output shared by later stages.
type Input struct {
	WAV       *wav.ParseResult
	RTP       *rtp.ParseResult
	Unwrapped *rtp.UnwrapResult
	Timescale uint32 // media timescale from the MP4 container metadata
}

// ParseOptions tunes stage-1 behaviour. Zero values select defaults.
type ParseOptions struct {
	Limits      model.Limits
	ReordWindow int
}

// ProbeTimescale reads the movie-header timescale from an MP4 metadata
// buffer. The RTP clock rate of the generated traces is carried there,
// mirroring how a packaged stream advertises its media clock.
func ProbeTimescale(buf []byte) (uint32, error) {
	var timescale uint32
	_, err := mp4.ReadBoxStructure(bytes.NewReader(buf),
		func(h *mp4.ReadHandle) (interface{}, error) {
			switch h.BoxInfo.Type {
			case mp4.BoxTypeMvhd():
				box, _, err := h.ReadPayload()
				if err != nil {
					return nil, err
				}
				timescale = box.(*mp4.Mvhd).Timescale
				return nil, nil
			}
			return h.Expand()
		})
	if err != nil {
		return 0, model.NewStructuredError(model.StageRTP, model.ReasonHeader,
			0, 0, "MP4 metadata unreadable: "+err.Error(), "reject input", err)
	}
	if timescale == 0 {
		return 0, model.NewStructuredError(model.StageRTP, model.ReasonHeader,
			0, 0, "MP4 metadata carries no timescale", "reject input", nil)
	}
	return timescale, nil
}

// Parse runs stage 1: WAV header and blocks, RTP trace decode and
// timestamp unwrap, plus the MP4 timescale probe. Fatal structured
// errors abort with exit code 1 at the CLI boundary; recoverable
// findings accumulate in the per-stage results.
func Parse(wavBuf, rtpBuf, mp4Buf []byte, opts ParseOptions) (*Input, error) {
	hdr, err := wav.ParseHeader(wavBuf)
	if err != nil {
		return nil, err
	}
	wres, err := wav.ParseBlocks(wavBuf, hdr, opts.Limits)
	if err != nil {
		return nil, err
	}
	rres, err := rtp.Decode(rtpBuf, opts.ReordWindow, opts.Limits)
	if err != nil {
		return nil, err
	}
	ts, err := ProbeTimescale(mp4Buf)
	if err != nil {
		return nil, err
	}
	return &Input{
		WAV:       wres,
		RTP:       rres,
		Unwrapped: rtp.Unwrap(rres.Packets),
		Timescale: ts,
	}, nil
}
