package batch

import (
	"io"
	"os"

	"replaycheck/internal/chunks"
	"replaycheck/internal/clock"
	"replaycheck/internal/container"
	"replaycheck/internal/events"
	"replaycheck/internal/fixture"
	"replaycheck/internal/index"
)

type processed struct {
	conclusion        index.FileConclusion
	block             index.BlockRecord
	anomalies         []clock.Anomaly
	tracks            int
	wrapCount         int
	maxDriftNS        int64
	maxBoundaryNS     int64
	plannedClipNS     int64
	actualClipNS      int64
	anomalyDurationNS int64
	inputDurationNS   int64
}

func processSample(s fixture.Sample) processed {
	p := processed{
		inputDurationNS: s.InputDurationNS,
		conclusion:      index.FileConclusion{Name: s.Name, File: s.Path, Status: "pass"},
		block:           index.BlockRecord{Key: s.Name, File: s.Path},
	}

	switch {
	case hasPrefix(s.Kind, "mp4"):
		p.processMP4(s)
	default:
		p.processRTP(s)
	}

	p.conclusion.Tracks = p.tracks
	p.conclusion.WrapCount = p.wrapCount
	p.conclusion.DriftMS = float64(p.maxDriftNS) / float64(clock.NanosecondsPerMillisecond)
	p.conclusion.MaxBoundaryMS = float64(p.maxBoundaryNS) / float64(clock.NanosecondsPerMillisecond)
	p.conclusion.Anomalies = p.anomalies
	p.conclusion.AnomalyCount = len(p.anomalies)
	for _, a := range p.anomalies {
		if a.EndNS > a.StartNS {
			p.anomalyDurationNS += a.EndNS - a.StartNS
		}
	}
	if p.conclusion.Status == "pass" && len(p.anomalies) > 0 {
		p.conclusion.Status = "warning"
	}
	return p
}

func (p *processed) fail(err error) {
	ce := clock.AsError(err)
	if ce.Path == "" {
		ce.Path = p.conclusion.File
	}
	p.anomalies = append(p.anomalies, clock.AnomalyFromError(ce, "failure"))
	p.conclusion.Status = "failed"
	p.conclusion.Message = ce.Error()
}

func (p *processed) processMP4(s fixture.Sample) {
	rep, anomalies, err := container.ProbePath(s.Path)
	if err != nil {
		p.fail(err)
		return
	}
	p.anomalies = append(p.anomalies, anomalies...)
	p.tracks = len(rep.Tracks)

	var timeline clock.Timeline
	found := false
	for _, tr := range rep.Tracks {
		if tr.DriftNS > p.maxDriftNS {
			p.maxDriftNS = tr.DriftNS
		}
		if len(tr.Timeline.Frames) > len(timeline.Frames) {
			timeline = tr.Timeline
			found = true
		}
	}
	if !found {
		p.fail(clock.NewError(clock.CodeMissingTrack, "no frame timeline", rep.MoovOffset).WithPath(s.Path))
		return
	}
	p.mapAndChunk(s, timeline)
}

func (p *processed) processRTP(s fixture.Sample) {
	data, err := readBounded(s.Path)
	if err != nil {
		p.fail(err)
		return
	}
	packets, parseErr := fixture.ParseRTPBytes(data)
	stamps := fixture.Stamps(packets)
	report := clock.UnwrapRTP(stamps, 90_000, 0)
	p.wrapCount = report.WrapCount
	p.anomalies = append(p.anomalies, report.Anomalies...)

	if parseErr != nil {
		ce := clock.AsError(parseErr)
		if len(report.Samples) > 0 {
			last := report.Samples[len(report.Samples)-1]
			nextNS, convErr := clock.Convert(last.Ticks+3000, 90_000)
			if convErr == nil {
				ce.StartNS = last.NS
				ce.EndNS = nextNS
			}
		}
		p.fail(ce)
	}

	if len(report.Samples) < 2 {
		if parseErr == nil {
			p.fail(clock.NewError(clock.CodeCorruptPacket, "RTP timeline has fewer than two packets", 0).WithPath(s.Path))
		}
		return
	}

	frames := make([]clock.Frame, len(report.Samples))
	for i, sm := range report.Samples {
		dur := int64(33_333_333)
		if i+1 < len(report.Samples) {
			dur = report.Samples[i+1].NS - sm.NS
		} else if i > 0 {
			dur = sm.NS - report.Samples[i-1].NS
		}
		frames[i] = clock.Frame{Index: i, DTSRaw: sm.Ticks, DTSNS: sm.NS, DurationNS: dur, ByteOffset: sm.Offset}
	}
	first, last := frames[0], frames[len(frames)-1]
	timeline := clock.Timeline{
		TrackID: 0,
		Timebase: clock.Timebase{
			Timescale: 90_000, RawStart: first.DTSRaw, RawEnd: last.DTSRaw + 3000,
			StartNS: first.DTSNS, EndNS: last.DTSNS + last.DurationNS,
		},
		Frames: frames,
	}
	p.mapAndChunk(s, timeline)
}

func (p *processed) mapAndChunk(s fixture.Sample, timeline clock.Timeline) {
	base := timeline.Frames[0].DTSNS
	ev := events.Event{
		ID: s.Name, StartNS: base + s.EventRelativeStartNS, EndNS: base + s.EventRelativeEndNS,
	}
	cut, err := events.MapEvent(ev, timeline)
	if err != nil {
		p.fail(err)
		return
	}
	p.maxBoundaryNS = 0
	if s.Kind == fixture.KindVFR {
		p.maxBoundaryNS = cut.BoundaryErrorNS
	}
	p.plannedClipNS = s.EventRelativeEndNS - s.EventRelativeStartNS
	p.actualClipNS = cut.CutEndNS - cut.CutStartNS
	p.block.StartNS = cut.CutStartNS
	p.block.EndNS = cut.CutEndNS

	plannedChunks := chunks.PlanContiguous(0, cut.CutStartNS, cut.CutEndNS, s.ChunkCount, nil)
	chunkErrs := chunks.Validate(plannedChunks, cut.CutStartNS, cut.CutEndNS)
	for _, ce := range chunkErrs {
		ce.Path = s.Path
		p.anomalies = append(p.anomalies, clock.AnomalyFromError(ce, "error"))
		p.conclusion.Status = "failed"
	}
}

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, clock.NewError(clock.CodeUnreadable, err.Error(), -1).WithPath(path)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, clock.NewError(clock.CodeUnreadable, err.Error(), -1).WithPath(path)
	}
	if st.Size() > container.MaxFileBytes {
		return nil, clock.NewError(clock.CodeTooLarge, "file exceeds bounded read limit", -1).WithPath(path)
	}
	b, err := io.ReadAll(io.LimitReader(f, container.MaxFileBytes+1))
	if err != nil {
		return nil, clock.NewError(clock.CodeUnreadable, err.Error(), -1).WithPath(path)
	}
	if int64(len(b)) > container.MaxFileBytes {
		return nil, clock.NewError(clock.CodeTooLarge, "file exceeds bounded read limit", -1).WithPath(path)
	}
	return b, nil
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
