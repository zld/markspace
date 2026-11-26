package main

import (
	"fmt"

	"replaykit/media/audit"
	"replaykit/media/fault"
	"replaykit/media/recovery"
	"replaykit/media/segment"
)

// runAudioFault reports exactly one lost and one duplicate packet.
func runAudioFault(root string) scenarioResult {
	const name = "audiofault"
	rep := audit.NewReport(name)
	ar := runAudio(lossDupStream())
	rep.AddAll(ar.anomalies())
	m := &rep.Metrics
	m.Lost = len(ar.check.Lost)
	m.Duplicate = len(ar.check.Duplicates)
	m.Late = len(ar.check.Late)
	ok := m.Lost == 1 && m.Duplicate == 1 && m.Late == 0 && len(ar.check.Packets) == 5
	line := fmt.Sprintf("[audiofault] lost=%d duplicate=%d late=%d accepted_unique=%d %s",
		m.Lost, m.Duplicate, m.Late, len(ar.check.Packets), boolOK(ok))
	code := 0
	if !ok {
		code = 1
	}
	return finish(name, root, 0, code, rep, ok, line)
}

// runTruncated proves one truncated box is reported by offset and the intact
// prefix still yields hash-verified segments.
func runTruncated(root string) scenarioResult {
	const name = "truncated"
	rep := audit.NewReport(name)
	layer, err := newLayer("720p", truncatedVideo(), 3000, nil)
	if err != nil {
		return finish(name, root, 0, 1, rep, false, "truncated: "+err.Error())
	}
	rep.AddAll(layer.Index.Anomalies)
	trunc := countCategory(layer.Scan.Anomalies, fault.CategoryTruncatedBox)

	events := []segment.Event{
		{ID: "early", AtNS: 1_000_000_000},
		{ID: "tail", AtNS: 3_000_000_000, PostNS: 500_000_000},
	}
	fan := segment.Fanout(events, []*segment.Layer{layer}, segment.Options{})
	rep.AddAll(fan.Anomalies)
	rep.Segments = append(rep.Segments, fan.Records()...)
	items := toItems(fan.Windows)
	res, rerr := recovery.Run(caseDir(root, name), items)
	if rerr != nil {
		return finish(name, root, 0, 1, rep, false, "truncated recovery: "+rerr.Error())
	}
	rep.AddAll(res.Anomalies)
	m := &rep.Metrics
	m.Truncated = trunc
	m.Segments = res.HashTotal
	m.HashMatched, m.HashTotal = res.HashMatched, res.HashTotal

	ok := trunc == 1 && fan.Unrecoverable == 0 && res.MatchedAll() && res.HashTotal == 2
	line := fmt.Sprintf("[truncated] truncated=%d index_complete=%t segments=%d hash_match=%s unrecoverable=%d %s",
		trunc, layer.Index.Complete, res.HashTotal, hashMatchFrac(res.HashMatched, res.HashTotal),
		fan.Unrecoverable, boolOK(ok))
	code := 0
	if !ok {
		code = 1
	}
	return finish(name, root, 0, code, rep, ok, line)
}

// runBadIndex proves a checksum-failing sidecar is discarded and rebuilt to
// the same digest as a fresh parse, then used for slicing.
func runBadIndex(root string) scenarioResult {
	const name = "badindex"
	rep := audit.NewReport(name)
	data := normalHigh()
	truth, err := indexOnly(data)
	if err != nil {
		return finish(name, root, 0, 1, rep, false, "badindex truth: "+err.Error())
	}
	_, tampered, err := tamperedSidecar(truth)
	if err != nil {
		return finish(name, root, 0, 1, rep, false, "badindex sidecar: "+err.Error())
	}
	layer, err := newLayer("720p", data, 3000, tampered)
	if err != nil {
		return finish(name, root, 0, 1, rep, false, "badindex layer: "+err.Error())
	}
	rep.AddAll(layer.Index.Anomalies)
	checksumFail := countCategory(layer.Index.Anomalies, fault.CategoryIndexChecksum)
	digestMatch := layer.Index.Digest == truth.Digest
	ignoredTamper := len(layer.Index.Track.Samples) > 0 && layer.Index.Track.Samples[0].DecodeTime == 0

	rep.Add(fault.New("720p", 0, fault.CategoryIndexRebuilt, fault.SeverityWarning,
		fault.ActionRebuiltIndex, truth.Track.Timescale, fault.Range{},
		"sidecar checksum failed; index rebuilt from valid fragments"))

	fan := segment.Fanout(proofEvents(), []*segment.Layer{layer}, segment.Options{})
	rep.AddAll(fan.Anomalies)
	items := toItems(fan.Windows)
	res, rerr := recovery.Run(caseDir(root, name), items)
	if rerr != nil {
		return finish(name, root, 0, 1, rep, false, "badindex recovery: "+rerr.Error())
	}
	rep.AddAll(res.Anomalies)
	m := &rep.Metrics
	m.IndexRebuilt = 1
	m.Segments = res.HashTotal
	m.HashMatched, m.HashTotal = res.HashMatched, res.HashTotal

	ok := checksumFail == 1 && digestMatch && ignoredTamper && res.MatchedAll() && res.HashTotal == 3
	line := fmt.Sprintf("[badindex] index_rebuilt=1 checksum_fail=%d digest_match=%t tampered_sidecar_ignored=%t hash_match=%s %s",
		checksumFail, digestMatch, ignoredTamper, hashMatchFrac(res.HashMatched, res.HashTotal), boolOK(ok))
	code := 0
	if !ok {
		code = 1
	}
	return finish(name, root, 0, code, rep, ok, line)
}

// runCorruptSkip exercises the explicit next-keyframe resynchronisation.
func runCorruptSkip(root string) scenarioResult {
	const name = "corruptskip"
	rep := audit.NewReport(name)
	layer, err := newLayer("720p", normalHigh(), 3000, nil)
	if err != nil {
		return finish(name, root, 0, 1, rep, false, "corruptskip: "+err.Error())
	}
	layer.CorruptFragments = map[int]bool{13: true}
	ev := segment.Event{ID: "jump", AtNS: 2_000_000_000}
	w, err := segment.PlanWindow(ev, layer, segment.Options{})
	if err != nil {
		return finish(name, root, 0, 1, rep, false, "corruptskip plan: "+err.Error())
	}
	rep.AddAll(w.Anomalies)
	items := toItems([]*segment.Window{w})
	res, rerr := recovery.Run(caseDir(root, name), items)
	if rerr != nil {
		return finish(name, root, 0, 1, rep, false, "corruptskip recovery: "+rerr.Error())
	}
	sawSkip := countCategory(w.Anomalies, fault.CategorySourceCorrupt) == 1
	ok := w.Partial && w.Uncovered != nil && sawSkip && w.HasKeyframe &&
		w.Keyframe.DecodeTime == 195000 && res.MatchedAll()
	line := fmt.Sprintf("[corrupt-skip] corrupt_skip=%d uncovered=1 next_keyframe=%d hash_match=%s %s",
		countCategory(w.Anomalies, fault.CategorySourceCorrupt), w.Keyframe.DecodeTime,
		hashMatchFrac(res.HashMatched, res.HashTotal), boolOK(ok))
	code := 0
	if !ok {
		code = 1
	}
	return finish(name, root, 0, code, rep, ok, line)
}
