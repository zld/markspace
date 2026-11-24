package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"

	"mediascan/internal/analyze"
	"mediascan/internal/clock"
	"mediascan/internal/manifest"
	"mediascan/internal/model"
	mp4inspect "mediascan/internal/mp4"
	"mediascan/internal/stream"
)

func (rn *runner) processItem(ctx context.Context, it workItem) (model.SegmentReport, error) {
	sr := model.SegmentReport{
		URI: it.URI, Period: it.Period,
		GlobalStartTicks: it.GlobalStart,
		GlobalEndTicks:   it.GlobalStart + it.ManifestDuration,
		DamageStartTicks: model.Unknown, DamageEndTicks: model.Unknown,
	}

	abs, err := manifest.SafeResolve(rn.root, it.URI)
	if err != nil {
		// Confinement violations are argument/security failures -> exit 1.
		return sr, err
	}

	h := sha256.New()
	n, derr := stream.Drain(ctx, abs, rn.budget, rn.chunk, func(_ int64, p []byte) error {
		_, wErr := h.Write(p)
		return wErr
	})
	if derr != nil {
		if ctx.Err() != nil {
			return sr, derr
		}
		sr.Faults = append(sr.Faults, unreadableFault(it.URI, derr))
		sr.Complete = false
		return sr, nil
	}
	sr.Bytes = n
	sr.ContentSHA256 = hex.EncodeToString(h.Sum(nil)[:])

	fallbackTS, initFaults := rn.ensureInit(it.InitURI)
	sr.Faults = append(sr.Faults, initFaults...)

	res, ierr := mp4inspect.InspectWith(abs, mp4inspect.Options{
		Segment: it.URI, FallbackTimescale: fallbackTS,
	})
	if ierr != nil {
		sr.Faults = append(sr.Faults, unreadableFault(it.URI, ierr))
		sr.Complete = false
		return sr, nil
	}
	sr.Kind = res.Kind
	if res.Timescale != 0 {
		sr.Timescale = res.Timescale
	} else {
		sr.Timescale = fallbackTS
	}
	sr.Faults = append(sr.Faults, res.Faults...)

	var as []analyze.Sample
	switch res.Kind {
	case "progressive":
		if len(res.Tracks) > 0 {
			tr := res.Tracks[0]
			sr.SampleCount = int64(len(tr.Samples))
			sr.SyncSamples = tr.SyncIndices
			for _, s := range tr.Samples {
				as = append(as, analyze.Sample{
					Index: s.Index, TimeStartTicks: s.TimeUnified,
					DurationTicks: s.DurationTicks, Sync: s.Sync,
				})
			}
		}
	case "fragmented":
		for _, fr := range res.Fragments {
			rn.handleWrapDrift(&sr, it, res.Timescale, fr)
			for _, s := range fr.Samples {
				as = append(as, analyze.Sample{
					Index: s.Index, TimeStartTicks: s.TimeUnified,
					DurationTicks: s.DurationTicks, Sync: s.Sync,
				})
				sr.SampleCount++
			}
		}
	}

	gst := analyze.GOP(as)
	sr.GOPIntervals = gst.Intervals
	sr.GOPMinTicks = gst.MinTicks
	sr.GOPMaxTicks = gst.MaxTicks

	for _, f := range res.Faults {
		if f.SampleIndex >= 0 {
			ds, de := analyze.DamageRange(as, f.SampleIndex)
			sr.DamageStartTicks = ds
			sr.DamageEndTicks = de
			break
		}
	}

	sr.Complete = !model.HasErrors(sr.Faults)
	return sr, nil
}

func unreadableFault(uri string, err error) model.Fault {
	code := model.CodeSegmentUnreadable
	f := model.NewFault(code, uri, err.Error())
	var pe *fs.PathError
	if errors.As(err, &pe) {
		f.Message = pe.Err.Error() + ": " + pe.Path
	}
	f.OffsetBytes = model.Unknown
	f.TimeStart, f.TimeEnd = model.Unknown, model.Unknown
	return f
}

func (rn *runner) ensureInit(uri string) (uint32, []model.Fault) {
	if uri == "" {
		return 0, nil
	}
	if cached, ok := rn.initRes[uri]; ok {
		if cached == nil {
			return 0, nil
		}
		return cached.Timescale, cached.Faults
	}
	abs, err := manifest.SafeResolve(rn.root, uri)
	if err != nil {
		f := model.NewFault(model.CodePathEscape, uri, err.Error())
		rn.initRes[uri] = nil
		return 0, []model.Fault{f}
	}
	res, err := mp4inspect.InspectWith(abs, mp4inspect.Options{Segment: uri})
	if err != nil {
		f := model.NewFault(model.CodeMissingInit, uri, "initialization segment unreadable: "+err.Error())
		f.OffsetBytes = 0
		rn.initRes[uri] = nil
		return 0, []model.Fault{f}
	}
	rn.initRes[uri] = res
	return res.Timescale, res.Faults
}

func (rn *runner) handleWrapDrift(sr *model.SegmentReport, it workItem, ts uint32, fr mp4inspect.FragmentInfo) {
	if ts == 0 {
		ts = model.UnifiedTimescale
	}
	if rn.unwrap == nil {
		// Boundary window of roughly one source-timescale second.
		rn.unwrap = clock.NewUnwrapper(clock.RTP32Modulus, int64(ts))
		rn.drift = clock.NewDriftEstimator(50_000) // bounded to +/-5%
	}
	v, ev, ok := rn.unwrap.Unwrap(int64(fr.BaseDecodeRaw))
	if ev != nil {
		sr.WrapEvents++
	}
	if !ok {
		f := model.NewWarning(model.CodeWrapEvidence, it.URI,
			"backward tfdt jump lacks modulus-boundary evidence; not unwrapped")
		f.OffsetBytes = fr.BoxOffset
		sr.Faults = append(sr.Faults, f)
		return
	}
	if !rn.haveOrigin {
		rn.haveOrigin = true
		rn.originUnwrapped = v
		rn.originRef = it.GlobalStart
	}
	mediaRel, err := clock.ToUnified(v-rn.originUnwrapped, ts)
	if err != nil {
		return
	}
	rn.drift.Observe(it.GlobalStart-rn.originRef, mediaRel)
}

func (rn *runner) finalizeDrift(ss []model.SegmentReport) (int64, int) {
	wraps := wrapFromSegments(ss)
	if rn != nil && rn.drift != nil {
		if ppm, ok := rn.drift.EstimatePPM(); ok {
			rn.driftPPM = ppm
			for i := range ss {
				if ss[i].Kind == "fragmented" {
					ss[i].DriftPPM = ppm
				}
			}
			return ppm, wraps
		}
	}
	return driftFromSegments(ss), wraps
}

func followBestVariant(root string, h *manifest.HLS) (*manifest.HLS, error) {
	best, bestBW := -1, -1
	for i, v := range h.Variants {
		if v.Bandwidth > bestBW {
			bestBW, best = v.Bandwidth, i
		}
	}
	if best < 0 {
		return h, nil
	}
	abs, err := manifest.SafeResolve(root, h.Variants[best].URI)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	return manifest.ParseHLS(bytes.NewReader(b))
}
