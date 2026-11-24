package pipeline

import (
	"fmt"

	"mediarc/internal/demux"
	"mediarc/internal/fixture"
	"mediarc/internal/model"
	"mediarc/internal/remux"
	rtprecover "mediarc/internal/rtp"
	"mediarc/internal/timeline"
	"mediarc/internal/track"
	"mediarc/internal/verify"
)

type Outcome struct {
	Options    fixture.Options
	InputBytes []byte
	Archive    *model.Archive
	Plan       *track.Plan
	Timeline   timeline.Result
	RTP        rtprecover.Report
	Remux      *remux.Result
	Comparison *verify.Comparison
	Report     verify.Report
}

func RunDemo(opt fixture.Options) (*Outcome, error) {
	archive := fixture.GenerateArchive(opt)
	encoded, err := demux.EncodeContainer(archive)
	if err != nil {
		return nil, err
	}
	container, err := demux.Demux(encoded)
	if err != nil {
		return nil, err
	}
	src := container.Archive

	plan, err := track.PlanMapping(src.Tracks, track.DefaultRules(src.Tracks))
	if err != nil {
		return nil, err
	}

	rtpReport, err := recoverRTP(src, 128)
	if err != nil {
		return nil, err
	}

	engine := timeline.NewEngine(opt.LateMS, opt.LateMS+240)
	rtpEvent := timeline.Event{
		ID: "rtp-recovery-result", TrackID: 0, PTS: model.Milliseconds(0),
		Kind: "rtp_report", Payload: []byte(fmt.Sprintf("recovered=%d lost=%d", rtpReport.Recovered, rtpReport.InjectedLost)),
	}
	if _, err := engine.Add(rtpEvent); err != nil {
		return nil, err
	}

	// Feed regular metadata first, then a synthetic high-water marker at
	// latePTS+lateMS, then the late subtitle. This deterministically exercises
	// backtracking for the requested lateness.
	var late *model.TimedEvent
	for i := range src.Events {
		ev := src.Events[i]
		if ev.ID == "sub-late" {
			late = &ev
			continue
		}
		if _, err := engine.Add(timeline.Event{
			ID: ev.ID, TrackID: ev.TrackID, PTS: ev.PTS, Kind: string(ev.Kind),
			Payload: ev.Payload, SourceOffset: ev.SourceOffset,
		}); err != nil {
			return nil, err
		}
	}
	if late != nil {
		markerPTS := late.PTS.Millis() + opt.LateMS
		if _, err := engine.Add(timeline.Event{
			ID: "watermark-marker", TrackID: late.TrackID, PTS: model.Milliseconds(markerPTS),
			Kind: "watermark", SourceOffset: late.SourceOffset,
		}); err != nil {
			return nil, err
		}
		if _, err := engine.Add(timeline.Event{
			ID: late.ID, TrackID: late.TrackID, PTS: late.PTS, Kind: string(late.Kind),
			Payload: late.Payload, SourceOffset: late.SourceOffset,
		}); err != nil {
			return nil, err
		}
	}
	tlResult := engine.Result()

	out, err := remux.Remux(src, plan, &tlResult)
	if err != nil {
		return nil, err
	}
	cmp, err := verify.CompareRemux(src, out.Bytes, plan)
	if err != nil {
		return nil, err
	}

	rep := cmp.Report
	video := src.Track(1)
	audio := src.Track(2)
	if video != nil && audio != nil {
		rep.AVSyncMS = verify.AVSyncDriftMS(
			src.SamplesFor(1), video.TimeBase,
			src.SamplesFor(2), audio.TimeBase,
		)
	}
	rep.P95LatencyMS = verify.Percentile95NearestRank(src.ProcessingLatenciesMS)
	rep.PacketRecoveryRate = rtpReport.RecoveryRate
	rep.RewindCount = tlResult.RewindCount
	rep.DuplicatePackets = rtpReport.Duplicates
	rep.ReorderedPackets = rtpReport.Reordered
	rep.RecoveredPackets = rtpReport.Recovered
	rep.InjectedLossPackets = rtpReport.InjectedLost
	rep.UnrecoverablePackets = rtpReport.Unrecoverable
	rep.DeterministicHash = out.SHA256
	cmp.Report = rep

	return &Outcome{
		Options: opt, InputBytes: encoded, Archive: src, Plan: plan,
		Timeline: tlResult, RTP: rtpReport, Remux: out, Comparison: cmp, Report: rep,
	}, nil
}

func recoverRTP(a *model.Archive, window uint64) (rtprecover.Report, error) {
	r := rtprecover.NewRecoverer(rtprecover.Config{Window: window})
	for _, p := range a.RTPPackets {
		if p.InjectedLoss {
			r.NoteInjectedLoss(p)
		}
	}
	for _, p := range a.RTPPackets {
		if !p.InjectedLoss {
			if err := r.AcceptModel(p, false); err != nil {
				return r.Finalize(), err
			}
		}
	}
	for _, p := range a.RTPPackets {
		if p.InjectedLoss {
			if err := r.AcceptModel(p, true); err != nil {
				return r.Finalize(), err
			}
		}
	}
	rep := r.Finalize()
	if rep.Unrecoverable > 0 {
		return rep, r.UnrecoverableError(rep)
	}
	return rep, nil
}
