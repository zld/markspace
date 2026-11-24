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

type CaseResult struct {
	Name           string                `json:"case"`
	Offset         *int64                `json:"source_offset,omitempty"`
	RewindCount    int                   `json:"rewind_count,omitempty"`
	ConflictFields []string              `json:"conflict_fields,omitempty"`
	Unrecoverable  int                   `json:"unrecoverable_packets,omitempty"`
	Tracks         []verify.TrackSummary `json:"tracks,omitempty"`
	Timeline       *timeline.Result      `json:"timeline,omitempty"`
	RTP            *rtprecover.Report    `json:"rtp,omitempty"`
	Err            error                 `json:"-"`
}

func RunCase(name string) (*CaseResult, error) {
	switch name {
	case "truncated":
		return truncatedCase()
	case "late-boundary":
		return lateBoundaryCase()
	case "color-conflict":
		return colorConflictCase()
	case "loss-over-window":
		return lossOverWindowCase()
	default:
		return nil, model.NewError(model.StageCLI, model.CodeInvalidArgument,
			fmt.Sprintf("unknown verify case %q", name), nil)
	}
}

func truncatedCase() (*CaseResult, error) {
	a := fixture.GenerateArchive(fixture.DefaultOptions())
	raw, err := demux.EncodeContainer(a)
	if err != nil {
		return nil, err
	}
	// Cut inside the MRCA payload while leaving its eight-byte box header and
	// declared length intact. Demux must report the actual EOF offset.
	cut := 160
	if cut > len(raw) {
		cut = len(raw) / 2
	}
	_, err = demux.Demux(demux.Truncate(raw, cut))
	if err == nil {
		return nil, model.NewError(model.StageVerify, model.CodeCorruptPayload, "truncated fixture was accepted", nil)
	}
	me, ok := model.AsMediaError(err)
	if !ok || me.SourceOffset == nil {
		return nil, fmt.Errorf("truncation error lacks offset: %w", err)
	}
	off := *me.SourceOffset
	return &CaseResult{Name: "truncated", Offset: &off, Err: err}, err
}

func lateBoundaryCase() (*CaseResult, error) {
	engine := timeline.NewEngine(40, 100)
	feed := []timeline.Event{
		ms("e0", 0), ms("e100", 100), ms("e50", 50),
		ms("e200", 200), ms("e150", 150),
	}
	for _, ev := range feed {
		if _, err := engine.Add(ev); err != nil {
			return nil, err
		}
	}
	res, err := engine.Add(timeline.Event{
		ID: "e10-too-late", TrackID: 7, PTS: model.Milliseconds(10),
		Kind: "subtitle", SourceOffset: 77010,
	})
	out := engine.Result()
	if err == nil {
		err = model.NewError(model.StageTimeline, model.CodeLateBoundary, "late boundary event was accepted", nil)
	}
	return &CaseResult{Name: "late-boundary", RewindCount: out.RewindCount, Timeline: res, Err: err}, err
}

func ms(id string, v int64) timeline.Event {
	return timeline.Event{ID: id, TrackID: 3, PTS: model.Milliseconds(v), SourceOffset: 7000 + v}
}

func colorConflictCase() (*CaseResult, error) {
	opt := fixture.DefaultOptions()
	opt.ColorConflict = true
	a := fixture.GenerateArchive(opt)
	raw, err := demux.EncodeContainer(a)
	if err != nil {
		return nil, err
	}
	c, err := demux.Demux(raw)
	if err != nil {
		return nil, err
	}
	plan, err := track.PlanMapping(c.Archive.Tracks, track.DefaultRules(c.Archive.Tracks))
	if err != nil {
		return nil, err
	}
	_, err = remux.Remux(c.Archive, plan, nil)
	if err == nil {
		err = model.NewError(model.StageRemux, model.CodeColorConflict, "conflicting color metadata was accepted", nil)
	}
	me, _ := model.AsMediaError(err)
	fields := []string{}
	if me != nil {
		fields = me.Fields
	}
	return &CaseResult{
		Name: "color-conflict", ConflictFields: fields,
		Tracks: verify.SummarizeTracks(c.Archive), Err: err,
	}, err
}

func lossOverWindowCase() (*CaseResult, error) {
	lost, progress := fixture.LossOverWindowPackets()
	r := rtprecover.NewRecoverer(rtprecover.Config{Window: 4})
	for _, p := range lost {
		r.NoteInjectedLoss(p)
	}
	for _, p := range progress {
		if err := r.AcceptModel(p, false); err != nil {
			return nil, err
		}
	}
	rep := r.Finalize()
	rtpErr := r.UnrecoverableError(rep)
	if rtpErr == nil {
		rtpErr = model.NewError(model.StageRTP, model.CodeLossOverWindow, "expected over-window loss", nil)
		rtpErr.Unrecoverable = rep.Unrecoverable
	}
	tlErr := model.NewError(model.StageTimeline, model.CodeLossOverWindow,
		"unrecoverable RTP gap closed the timeline recovery window", rtpErr)
	tlErr.Unrecoverable = rep.Unrecoverable
	if me, ok := model.AsMediaError(rtpErr); ok {
		tlErr.Timestamp = me.Timestamp
		tlErr.SourceOffset = me.SourceOffset
	}
	return &CaseResult{
		Name: "loss-over-window", Unrecoverable: rep.Unrecoverable,
		RTP: &rep, Err: tlErr,
	}, tlErr
}
