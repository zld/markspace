package main

import (
	"fmt"
	"path/filepath"

	"replaykit/internal/fixture"
	"replaykit/media/audit"
	"replaykit/media/recovery"
	"replaykit/media/segment"
)

// runNormal proves the clean path: exact clock roundtrips, 100% keyframe hits,
// zero cross-layer skew, 3 events x 2 layers = 6 segments all hash-verified,
// clean Opus and AAC audio and an anomaly-free manifest.
func runNormal(root string) scenarioResult {
	const name = "normal"
	rep := audit.NewReport(name)
	dir := filepath.Join(root, name)

	// Exercise the events.json input path by writing and reloading it.
	eventsPath := filepath.Join(dir, "input", "events.json")
	if err := fixture.WriteJSON(eventsPath, fixture.ProofEventFile()); err != nil {
		return finish(name, root, 0, 1, rep, false, "normal: events.json write failed: "+err.Error())
	}
	ef, err := fixture.LoadEvents(eventsPath)
	if err != nil {
		return finish(name, root, 0, 1, rep, false, "normal: events.json load failed: "+err.Error())
	}
	events := eventsFromEF(ef)

	hiData, loData := fixture.NormalHigh(), fixture.NormalLow()
	hi, err := newLayer("720p", hiData, int64(fixture.HiFrameTicks), nil)
	if err != nil {
		return finish(name, root, 0, 1, rep, false, "normal: high layer: "+err.Error())
	}
	lo, err := newLayer("360p", loData, int64(fixture.LoFrameTicks), nil)
	if err != nil {
		return finish(name, root, 0, 1, rep, false, "normal: low layer: "+err.Error())
	}
	layers := []*segment.Layer{hi, lo}
	for _, l := range layers {
		rep.AddAll(l.Index.Anomalies)
	}

	// Exact NS -> tick -> NS roundtrips for trigger and both expanded edges.
	rtOK, rtTotal := 0, 0
	for _, ev := range events {
		for _, ns := range []int64{ev.AtNS, ev.AtNS - ev.PreNS, ev.AtNS + ev.PostNS} {
			for _, l := range layers {
				_, _, exact, rerr := segment.RoundtripNS(l.Clock, ns)
				rtTotal++
				if rerr == nil && exact {
					rtOK++
				}
			}
		}
	}

	fan := segment.Fanout(events, layers, segment.Options{})
	rep.AddAll(fan.Anomalies)
	rep.Boundaries = append(rep.Boundaries, fan.Boundaries...)
	rep.Segments = append(rep.Segments, fan.Records()...)

	items := toItems(fan.Windows)
	res, err := recovery.Run(dir, items)
	if err != nil {
		return finish(name, root, 0, 1, rep, false, "normal: recovery run: "+err.Error())
	}
	rep.AddAll(res.Anomalies)

	// Clean Opus + AAC audio must carry no defects or duration errors.
	opus := runAudio(fixture.OpusClean(40))
	aac := runAudio(fixture.AACClean(30))
	audioAnom := append(opus.anomalies(), aac.anomalies()...)
	rep.AddAll(audioAnom)

	m := &rep.Metrics
	m.RoundtripOK, m.RoundtripTotal, m.RoundtripPct = rtOK, rtTotal, pct(rtOK, rtTotal)
	m.IndexLookups, m.IndexHits, m.IndexHitsPct = fan.LookupTotal, fan.LookupHits, pct(fan.LookupHits, fan.LookupTotal)
	m.BoundarySkewTicks = fan.MaxSkewTicks
	m.Segments = res.HashTotal
	m.HashMatched, m.HashTotal = res.HashMatched, res.HashTotal

	ok := rtOK == rtTotal && pct(rtOK, rtTotal) == 100 &&
		fan.LookupHits == fan.LookupTotal && pct(fan.LookupHits, fan.LookupTotal) == 100 &&
		fan.MaxSkewTicks == 0 && fan.Unrecoverable == 0 &&
		len(items) == 6 && res.HashMatched == 6 && res.HashTotal == 6 &&
		len(audioAnom) == 0

	line := fmt.Sprintf(
		"[normal] roundtrip=%d%% (%d/%d) index_hits=%d%% (%d/%d) boundary_skew_ticks=%d segments=%d hash_match=%s anomalies=%d audio=opus40+aac30 %s",
		m.RoundtripPct, rtOK, rtTotal, m.IndexHitsPct, fan.LookupHits, fan.LookupTotal,
		fan.MaxSkewTicks, m.Segments, hashMatchFrac(res.HashMatched, res.HashTotal),
		len(audioAnom), boolOK(ok))
	code := 0
	if !ok {
		code = 1
	}
	return finish(name, root, 0, code, rep, ok, line)
}
