package main

import (
	"fmt"

	"replaykit/internal/fixture"
	"replaykit/media/audit"
	"replaykit/media/fault"
	"replaykit/media/segment"
)

// runUnrecoverable injects an event beyond all covered media. The interval
// cannot be recovered, so the production status is 2 while the manifest is
// still written to disk.
func runUnrecoverable(root string) scenarioResult {
	const name = "unrecoverable"
	rep := audit.NewReport(name)
	layer, err := newLayer("720p", normalHigh(), 3000, nil)
	if err != nil {
		return finish(name, root, 2, 1, rep, false, "unrecoverable layer: "+err.Error())
	}
	ev := segment.Event{ID: "beyond-end", AtNS: 9_000_000_000} // 9s > 6.67s clip
	fan := segment.Fanout([]segment.Event{ev}, []*segment.Layer{layer}, segment.Options{})
	rep.AddAll(fan.Anomalies)
	rep.Segments = append(rep.Segments, fan.Records()...)
	rep.Metrics.Unrecoverable = fan.Unrecoverable

	ok := fan.Unrecoverable == 1
	line := fmt.Sprintf("[unrecoverable] unrecoverable=%d manifest_kept=true expected_exit=2 %s",
		fan.Unrecoverable, boolOK(ok))
	// ExpectedCode 2; finish still persists the manifest ("保留清单").
	return finish(name, root, 2, 2, rep, ok, line)
}

// runFatal feeds a container with no mdhd timescale: no time mapping exists,
// which is an input fatal error mapped to exit status 1.
func runFatal(root string) scenarioResult {
	const name = "fatal"
	rep := audit.NewReport(name)
	_, err := newLayer("broken", fixture.NoTimescaleVideo(), 3000, nil)
	isFatal := err != nil && fault.IsFatal(err)
	if isFatal {
		rep.Note("fatal as expected: %v", err)
		rep.Add(fault.New("broken", 0, fault.CategorySegmentFail, fault.SeverityFatal,
			fault.ActionNone, 0, fault.Range{}, err.Error()))
	}
	ok := isFatal
	line := fmt.Sprintf("[fatal] time_mapping_available=%t expected_exit=1 %s", !isFatal, boolOK(ok))
	return finish(name, root, 1, 1, rep, ok, line)
}
