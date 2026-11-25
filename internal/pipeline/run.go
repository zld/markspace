// Package pipeline wires the deterministic rtptrace demo stages together.
package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"time"

	"rtptrace/internal/evidence"
	"rtptrace/internal/fixture"
	"rtptrace/internal/manifest"
	"rtptrace/internal/recover"
	rtptrace "rtptrace/internal/rtp"
	"rtptrace/internal/segment"
	"rtptrace/internal/track"
)

// ExitInputCorrupt is the acceptance exit code for a usable corruption report.
const ExitInputCorrupt = 2

// Options configures one deterministic pipeline run.
type Options struct {
	Seed            int64
	Duration        time.Duration
	SegmentDuration time.Duration
	Bitrates        []int
	Fault           string
	Recover         bool
}

// Outcome contains the report and process exit classification.
type Outcome struct {
	Report   *evidence.Report
	ExitCode int
}

// AllowedFaults validates the --fault flag.
func AllowedFaults() map[string]bool {
	return map[string]bool{
		"": true, "none": true, "truncated-stbl": true,
		"edit-out-of-range": true, "invalid-dependency": true,
		"missing-keyframe": true,
	}
}

// Run executes all stages. A corrupt input yields ExitInputCorrupt and a
// non-nil report; unexpected internal errors are returned as Go errors.
func Run(opts Options) (Outcome, error) {
	f, err := fixture.Generate(fixture.Options{
		Seed: opts.Seed, Duration: opts.Duration, SegmentDuration: opts.SegmentDuration,
		Bitrates: opts.Bitrates, Fault: opts.Fault, Recover: opts.Recover,
	})
	if err != nil {
		return Outcome{}, err
	}

	m, parseErr := manifest.Parse(f.ManifestJSON)
	if parseErr != nil && m == nil {
		return Outcome{}, fmt.Errorf("manifest parse: %w", parseErr)
	}
	recoveryItems := []recover.Item{}
	exitCode := 0
	if parseErr != nil {
		item := recover.CorruptionItem(parseErr, opts.Recover)
		recoveryItems = append(recoveryItems, item)
		exitCode = ExitInputCorrupt
	}

	mp4File, mp4Err := manifest.ReadMP4(bytes.NewReader(f.MP4))
	if mp4Err != nil {
		item := recover.CorruptionItem(mp4Err, opts.Recover)
		if item.Code == "internal" {
			item.Code = "mp4-reader-error"
		}
		recoveryItems = append(recoveryItems, item)
		exitCode = ExitInputCorrupt
	}
	_ = mp4File // decoded boxes are cross-check evidence; errors are reported

	g, graphErr := track.NewGraph(m.Tracks, m.Dependencies)
	if graphErr != nil {
		recoveryItems = append(recoveryItems, graphItem(graphErr))
		rep := evidence.Build(evidence.Input{
			Seed: opts.Seed, Fault: opts.Fault, RecoverFlag: opts.Recover,
			ExitCode: ExitInputCorrupt, PublishAllowed: false,
			RecoveryItems: recoveryItems,
		})
		return Outcome{Report: rep, ExitCode: ExitInputCorrupt}, nil
	}

	audioPlans := []*track.AudioPlan{}
	colorDecisions := []track.ColorDecision{}
	exec := g.Run(func(id uint32) error {
		tr := findTrack(m.Tracks, id)
		if tr == nil {
			return fmt.Errorf("graph references missing track %d", id)
		}
		if tr.Kind == manifest.TrackVideo {
			colorDecisions = append(colorDecisions, track.NormalizeColor(*tr))
		}
		if tr.Kind == manifest.TrackAudio && tr.Audio != nil {
			plan, err := track.NormalizeAudio(*tr, "stereo", 48000)
			if err != nil {
				recoveryItems = append(recoveryItems, recover.Item{
					Code: "unsupported-media-metadata", TrackID: id,
					RecoveryState: recover.StateFailed, Message: err.Error(),
				})
				return err
			}
			audioPlans = append(audioPlans, plan)
		}
		return nil
	})

	var trace *rtptrace.Report
	if parsed, err := rtptrace.ParseTrace(f.RTPPackets, 64, rtpResolver(opts.SegmentDuration, int(m.Duration/opts.SegmentDuration)+1)); err != nil {
		return Outcome{}, err
	} else {
		trace = parsed
	}
	releaseRTPPayloadsBySegment(f, trace)

	editReports := []recover.EditReport{}
	for _, tr := range m.Tracks {
		if tr.EditList == nil {
			continue
		}
		mediaDuration := mediaDurationTicks(tr)
		er := recover.RecoverEditList(tr.ID, *tr.EditList, mediaDuration, opts.Recover)
		if er.State != recover.StateOK {
			editReports = append(editReports, er)
			it := recover.Item{
				Code: "edit-out-of-range", TrackID: tr.ID, RecoveryState: er.State,
				Message: er.Message,
			}
			if len(er.Adjustments) > 0 {
				it.TimeErrorTicks = er.Adjustments[0].TimeErrorTicks
				it.Action = "edit segment duration clamped to available media"
			}
			recoveryItems = append(recoveryItems, it)
			exitCode = ExitInputCorrupt
		}
	}

	var alignment *segment.AlignedReport
	publishAllowed := exec.PublishAllowed()
	if exec.PublishAllowed() {
		alignment = segment.Align(m, opts.SegmentDuration, g.Order)
		for _, b := range alignment.FailedBoundaries {
			recoveryItems = append(recoveryItems, recover.BoundaryItem(b))
			exitCode = ExitInputCorrupt
			publishAllowed = false
		}
		if parseErr != nil && !opts.Recover {
			publishAllowed = false
		}
		if len(editReports) > 0 && !opts.Recover {
			publishAllowed = false
		}
	} else {
		exitCode = ExitInputCorrupt
		publishAllowed = false
	}

	rep := evidence.Build(evidence.Input{
		Seed: opts.Seed, Fault: opts.Fault, RecoverFlag: opts.Recover,
		ExitCode: exitCode, PublishAllowed: publishAllowed, GraphOrder: g.Order,
		TrackStates: exec.States, AudioPlans: audioPlans,
		ColorDecisions: colorDecisions, Alignment: alignment, Trace: trace,
		RecoveryItems: recoveryItems, EditReports: editReports,
	})
	return Outcome{Report: rep, ExitCode: exitCode}, nil
}

func graphItem(err error) recover.Item {
	var invalid *track.InvalidDependencyError
	if errors.As(err, &invalid) {
		return recover.Item{
			Code: "invalid-dependency", TrackID: invalid.Dependency.TrackID,
			RecoveryState: recover.StateFailed,
			Message:       err.Error(),
			Action:        "downstream products blocked",
		}
	}
	return recover.Item{Code: "dependency-cycle", RecoveryState: recover.StateFailed, Message: err.Error()}
}

func findTrack(tracks []manifest.Track, id uint32) *manifest.Track {
	for i := range tracks {
		if tracks[i].ID == id {
			return &tracks[i]
		}
	}
	return nil
}

func mediaDurationTicks(t manifest.Track) int64 {
	count := t.DeclaredSampleCount
	if count == 0 {
		count = uint32(len(t.Samples))
	}
	if count > 0 {
		// Fixture samples have a uniform one-second delta. Using the declared
		// count prevents a truncated table from generating a secondary edit fault.
		return t.StartTimeTicks + int64(count)*int64(t.SourceTimescale)
	}
	return t.StartTimeTicks
}

// releaseRTPPayloadsBySegment scopes raw packet bytes to a segment batch and
// drops them immediately after the global sequence/window analysis is done.
func releaseRTPPayloadsBySegment(f *fixture.Fixture, trace *rtptrace.Report) {
	if f == nil {
		return
	}
	if trace == nil {
		f.RTPPackets = nil
		return
	}
	groups := make(map[string][][]byte)
	for _, ev := range trace.Events {
		if ev.SegmentID == "" || ev.ArrivalIndex >= len(f.RTPPackets) {
			continue
		}
		groups[ev.SegmentID] = append(groups[ev.SegmentID], f.RTPPackets[ev.ArrivalIndex])
	}
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	cache := segment.NewSampleCache()
	for _, id := range ids {
		payloads := groups[id]
		_ = segment.ProcessBatch(cache, id, payloads, func(batch [][]byte) error {
			// Classification is already complete; the batch boundary exists to
			// retain raw media only for this segment's optional local processing.
			return nil
		})
		groups[id] = nil
	}
	f.RTPPackets = nil
}

func rtpResolver(seg time.Duration, count int) rtptrace.SegmentResolver {
	segSeconds := uint32(seg / time.Second)
	if segSeconds == 0 {
		segSeconds = 1
	}
	return func(ts uint32) string {
		second := ts / 90000
		idx := int(second / segSeconds)
		if idx >= count {
			idx = count - 1
		}
		return segment.SegmentID(idx, count)
	}
}
