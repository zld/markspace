package pipeline

import (
	"os"
	"sort"

	"mediaaudit/internal/fixtures"
	"mediaaudit/internal/model"
	"mediaaudit/internal/timecode"

	"github.com/pion/rtp"
)

// RTPTrackResult is the outcome of one RTP track's clock audit. A drift
// failure marks only this track; the caller keeps processing others.
type RTPTrackResult struct {
	TrackID    string
	Packets    int
	Wraps      int
	WrapAt     []int
	Drift      model.Rat
	Correction model.Rat
	PPM        string
	Exceeded   bool
	Failed     bool
	Error      *model.Error
}

// RTPAudit is the aggregate result of auditing every RTP fixture.
type RTPAudit struct {
	Tracks []RTPTrackResult
	Errors model.Errors
}

// AuditRTPStreams unwraps each stream's timestamps and checks drift against
// the configured threshold. Each stream is isolated: one failing track never
// suppresses another's result. Paths are processed in sorted track-ID order
// so the terminal output is deterministic.
func AuditRTPStreams(paths map[string]string, maxPPM int64) RTPAudit {
	out := RTPAudit{}
	ids := make([]string, 0, len(paths))
	for id := range paths {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		path := paths[id]
		data, err := os.ReadFile(path)
		if err != nil {
			e := model.NewError(model.CodeIO, "pipeline", id, model.InputPos{File: path},
				"cannot read RTP stream: %v", err)
			out.Errors.Add(e)
			out.Tracks = append(out.Tracks, RTPTrackResult{TrackID: id, Failed: true, Error: e})
			continue
		}
		res := auditOneRTP(id, data, maxPPM)
		if res.Error != nil {
			out.Errors.Add(res.Error)
		}
		out.Tracks = append(out.Tracks, res)
	}
	return out
}

func auditOneRTP(id string, data []byte, maxPPM int64) RTPTrackResult {
	res := RTPTrackResult{TrackID: id}
	h, samples, err := fixtures.ParseRTPStream(data)
	if err != nil {
		res.Failed = true
		res.Error = model.NewError(model.CodeTimelineAlign, "pipeline", id, model.InputPos{},
			"malformed RTP stream: %v", err)
		return res
	}
	if h.Timescale == 0 || h.RefTimescale == 0 {
		res.Failed = true
		res.Error = model.NewError(model.CodeTimelineAlign, "pipeline", id, model.InputPos{},
			"RTP stream declares a zero timescale")
		return res
	}
	pkts := make([]*rtp.Packet, len(samples))
	for i := range samples {
		pkts[i] = samples[i].Packet
	}
	ext, wraps, uerr := timecode.ExtendRTPStream(pkts)
	if uerr != nil {
		res.Failed = true
		res.Error = uerr.(*model.Error)
		return res
	}
	res.Packets = len(samples)
	res.Wraps = len(wraps)
	res.WrapAt = wraps

	track := model.Track{ID: id, Kind: model.KindRTP}
	driftSamples := make([]timecode.DriftSample, 0, len(samples))
	// Compare elapsed spans against the first packet so absolute epochs (the
	// initial 32-bit value) do not masquerade as drift.
	mediaOrigin := ext[0]
	for i, s := range samples {
		media := ratDiv(int64(ext[i]-mediaOrigin), int64(h.Timescale))
		ref := ratDiv(int64(s.RefTick), int64(h.RefTimescale))
		driftSamples = append(driftSamples, timecode.DriftSample{Media: media, Ref: ref})
	}
	rep, derr := timecode.EstimateDrift(track, driftSamples, timecode.DriftConfig{MaxPPM: maxPPM})
	if derr != nil {
		res.Failed = true
		res.Error = derr.(*model.Error)
		return res
	}
	res.Drift = rep.Drift
	res.Correction = rep.Correction
	res.PPM = rep.PPM()
	res.Exceeded = rep.Exceeded
	if rep.Exceeded {
		res.Failed = true
		res.Error = rep.DriftError()
	}
	return res
}

// ratDiv builds an exact ratio, falling back to zero only on a zero
// denominator, which the callers have already rejected.
func ratDiv(num, den int64) model.Rat {
	r, err := model.NewRat(num, den)
	if err != nil {
		return model.RatFromInt(0)
	}
	return r
}
