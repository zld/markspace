// Package archive wires the subsystems into one deterministic run: it
// generates the fixture (or reads a supplied MP4), feeds RTP packets through
// the reorderer, admits metadata and replayed events into the timeline, then
// renders frames, synthesises audio and assembles the manifest.
package archive

import (
	"fmt"
	"sort"

	"archive.example/mediakernel/internal/audio"
	"archive.example/mediakernel/internal/clock"
	"archive.example/mediakernel/internal/events"
	"archive.example/mediakernel/internal/fixture"
	"archive.example/mediakernel/internal/manifest"
	"archive.example/mediakernel/internal/metadata"
	"archive.example/mediakernel/internal/render"
	"archive.example/mediakernel/internal/rtp"
	"archive.example/mediakernel/internal/timeline"
)

// Config bounds one archive run.
type Config struct {
	Fixture         fixture.Kind
	Seed            int64
	LateWindowNS    int64
	RTPLateWindow   int64
	SubtitleTrackID uint32
	ChaptersTrackID uint32
	RenderConfig    render.Config
	AudioConfig     audio.Config
	SegmentSize     int
}

// RunResult is the complete outcome of one run.
type RunResult struct {
	Manifest  manifest.Manifest
	Frames    render.Sequence
	Audio     audio.Stats
	Timeline  *timeline.Timeline
	RTPStats  rtp.Stats
	RTPStatus string
	Report    Report
	Fatal     *events.Error
	Recovered bool
	Archive   fixture.Archive
	Document  *metadata.Document
}

// Report is the human-readable terminal summary of a run.
type Report struct {
	Fixture       string
	Seed          int64
	Frames        int
	FrameDuration int64
	MaxAlignErr   int64
	FramesOK      bool
	MeanByte      float64
	MinByte       uint8
	MaxByte       uint8
	Peak          float64
	PeakDBFS      float64
	AudioFinite   bool
	StateHash     string
	FramesHash    string
	ManifestHash  string
	Segments      int
	GapCount      int
	ErrorCount    int
	Committed     int
	Replays       int
	Late          int
	WatermarkNS   int64
	Recovered     bool
	RTPSummary    manifest.RTPSummary
	Notes         []string
	Errors        []*events.Error
	OutDir        string
	ManifestPath  string
}

// DefaultConfig builds a config for a fixture kind and seed.
func DefaultConfig(kind fixture.Kind, seed int64) Config {
	return Config{
		Fixture:         kind,
		Seed:            seed,
		LateWindowNS:    2_000_000_000, // 2s allowed lateness
		RTPLateWindow:   6,
		SubtitleTrackID: 3,
		ChaptersTrackID: 4,
		RenderConfig:    render.DefaultConfig(0),
		AudioConfig:     audio.DefaultConfig(0, fixture.CorpusSpanNS),
		SegmentSize:     8,
	}
}

// Run executes the whole pipeline for a generated fixture. It never panics:
// any fatal condition surfaces as a *events.Error.
func Run(cfg Config, outDir string) (RunResult, *events.Error) {
	arc, err := fixture.Build(cfg.Fixture, cfg.Seed)
	if err != nil {
		return RunResult{}, events.NewError(events.CodeFixture, "", 0, -1, "fixture", err.Error(), err)
	}
	res := RunResult{Archive: arc, Report: Report{Fixture: string(cfg.Fixture), Seed: cfg.Seed, OutDir: outDir}}
	res.Report.Notes = append(res.Report.Notes, arc.Notes...)

	tl, tlErr := timeline.New(timeline.Config{
		LateWindowNS:    cfg.LateWindowNS,
		CheckpointEvery: 8,
		MaxReplayEvents: 400,
	})
	if tlErr != nil {
		return res, tlErr
	}
	res.Timeline = tl

	// 1. Parse the MP4 metadata document. Truncation is recoverable; other
	//    tracks keep parsing.
	doc, perr := metadata.ParseBytes(arc.MP4)
	res.Document = doc
	// Document.Errors is the complete, de-duplicated error list; perr only
	// indicates whether a fatal condition or any structural damage occurred.
	for _, e := range doc.Errors {
		res.Report.Errors = append(res.Report.Errors, e)
	}
	if fe := doc.FatalErr(); fe != nil {
		res.Fatal = fe
		return res, fe
	}
	if perr != nil {
		if _, ok := perr.(*events.Error); !ok {
			wrapped := events.NewError(events.CodeMalformed, "", 0, -1, "parse", perr.Error(), perr)
			res.Fatal = wrapped
			res.Report.Errors = append(res.Report.Errors, wrapped)
			return res, wrapped
		}
	}

	// 2. Feed the RTP stream through the reorderer, collecting what becomes
	//    deliverable. Reordering, duplicates and losses are observed here; the
	//    resulting packets join the metadata events in one media-time order.
	reorder, rerr := rtp.New(rtp.Config{Track: events.TrackRTP, LateWindow: cfg.RTPLateWindow, MaxBuffer: 256, Warmup: 4})
	if rerr != nil {
		return res, rerr
	}
	var latePackets []rtp.Packet
	var delivered []rtp.Packet
	for _, p := range arc.RTP {
		push, e := reorder.PushRaw(p.Bytes, p.Offset)
		if e != nil {
			res.Report.Errors = append(res.Report.Errors, e)
			if e.Fatal() {
				res.Fatal = e
				return res, e
			}
			continue
		}
		for _, gap := range push.Lost {
			if err := tl.MarkGap(gap); err != nil {
				return res, err
			}
		}
		delivered = append(delivered, push.Deliver...)
		latePackets = append(latePackets, push.Late...)
	}
	flush := reorder.Flush()
	for _, gap := range flush.Lost {
		if err := tl.MarkGap(gap); err != nil {
			return res, err
		}
	}
	delivered = append(delivered, flush.Deliver...)

	// 3. Admit the merged stream. Both sources describe the same media
	//    instants; admitting them in one canonical order means the watermark
	//    advances monotonically and no event is spuriously "late". RTP packets
	//    that were queued late are admitted last, after their gaps exist, so
	//    they trigger a replay exactly as intended.
	if err := admitMerged(tl, doc.Events, delivered, &res); err != nil {
		if err.Fatal() {
			res.Fatal = err
			return res, err
		}
	}
	for _, pk := range latePackets {
		// The hole was declared lost before this packet arrived; admitting
		// the packet fills the gap and replays the touched interval.
		tl.ResolveGap(events.TrackRTP, events.KindRTP, uint64(pk.Seq))
		if err := admitRTPPacket(tl, pk, &res); err != nil {
			return res, err
		}
	}

	// 4. Finalise.
	if _, err := tl.Flush(); err != nil {
		res.Fatal = err
		return res, err
	}
	res.RTPStats = reorder.Stats()
	res.RTPStatus = string(reorder.Status())
	res.Recovered = partialRecovery(arc, tl, res.Report.Errors)
	res.Manifest, res.Report, res.Fatal = finishRun(res, cfg, outDir)
	return res, res.Fatal
}

// partialRecovery reports whether the run is "damaged but partially
// recovered": data was lost or cut and the gap stayed unresolved. Fully
// recovered losses (a late packet filled its declared hole) are not partial:
// the state matches the complete ordered input.
func partialRecovery(arc fixture.Archive, tl *timeline.Timeline, errs []*events.Error) bool {
	if len(tl.Gaps(true)) > 0 {
		return true
	}
	if arc.Truncated {
		return true
	}
	for _, e := range errs {
		if e != nil && !e.Fatal() {
			return true
		}
	}
	return false
}

// admitMerged admits metadata events and delivered RTP packets in one
// canonical media-time order so the watermark advances monotonically.
func admitMerged(tl *timeline.Timeline, metaEvents []events.Event, delivered []rtp.Packet, res *RunResult) *events.Error {
	type pending struct {
		ev    events.Event
		isRTP bool
		pkt   rtp.Packet
	}
	var queue []pending
	for _, e := range metaEvents {
		queue = append(queue, pending{ev: e})
	}
	for _, pk := range delivered {
		ev, err := rtpEvent(pk, res)
		if err != nil {
			return err
		}
		if ev == nil {
			continue // undecodable payload already reported
		}
		queue = append(queue, pending{ev: *ev, isRTP: true, pkt: pk})
	}
	sort.SliceStable(queue, func(i, j int) bool {
		return events.CompareEvents(queue[i].ev, queue[j].ev) < 0
	})
	for _, item := range queue {
		if _, err := tl.Admit(item.ev); err != nil {
			res.Report.Errors = append(res.Report.Errors, err)
			if err.Fatal() {
				return err
			}
		}
	}
	return nil
}

// admitRTPPacket decodes a generated RTP payload and admits it as a track
// event positioned on the unified nanosecond timeline.
func admitRTPPacket(tl *timeline.Timeline, pk rtp.Packet, res *RunResult) *events.Error {
	ev, err := rtpEvent(pk, res)
	if err != nil {
		return err
	}
	if ev == nil {
		return nil
	}
	if _, aerr := tl.Admit(*ev); aerr != nil {
		res.Report.Errors = append(res.Report.Errors, aerr)
		if aerr.Fatal() {
			res.Fatal = aerr
			return aerr
		}
	}
	return nil
}

// rtpEvent converts a delivered packet into a timeline event. A nil event
// means the payload was undecodable; the error has already been recorded and
// is recoverable.
func rtpEvent(pk rtp.Packet, res *RunResult) (*events.Event, *events.Error) {
	rec, ok := rtp.DecodePayload(pk.Payload)
	if !ok {
		e := events.NewError(events.CodeMalformed, events.TrackRTP, uint64(pk.Seq), pk.ByteOffset,
			"rtp-payload", "payload is not a generated record", nil)
		res.Report.Errors = append(res.Report.Errors, e)
		return nil, nil
	}
	kind, ok := kindFromPayload(rec.Kind)
	if !ok {
		e := events.NewError(events.CodeMalformed, events.TrackRTP, uint64(pk.Seq), pk.ByteOffset,
			"rtp-payload", fmt.Sprintf("unknown payload kind %d", rec.Kind), nil)
		res.Report.Errors = append(res.Report.Errors, e)
		return nil, nil
	}
	timescale := trackTimescale(rec.TrackID)
	at, cerr := clock.MapTicks(kindTrack(kind), int64(rec.Presentation), timescale, 0, clock.MaxTimelineNS,
		uint64(rec.Sequence), pk.ByteOffset)
	if cerr != nil {
		res.Report.Errors = append(res.Report.Errors, cerr)
		return nil, nil
	}
	dur, _ := clock.DurationNS(int64(rec.Duration), timescale)
	ev := events.Event{
		Track:      kindTrack(kind),
		Kind:       kind,
		At:         at,
		Duration:   dur,
		Seq:        uint64(rec.Sequence),
		Text:       rec.Text,
		Payload:    append([]byte(nil), rec.Data...),
		Source:     "rtp",
		ByteOffset: pk.ByteOffset,
	}
	return &ev, nil
}

func kindFromPayload(code rtp.PayloadKind) (events.Kind, bool) {
	switch code {
	case rtp.PayloadSubtitle:
		return events.KindSubtitle, true
	case rtp.PayloadChapter:
		return events.KindChapter, true
	case rtp.PayloadID3:
		return events.KindID3, true
	case rtp.PayloadSEI:
		return events.KindSEI, true
	default:
		return "", false
	}
}

// kindTrack names the timeline track for an RTP-delivered event. RTP is its
// own transport: its events live on the single rtp track, distinct from the
// container tracks the MP4 document contributes. The semantic kind is
// preserved on the event so manifests can still classify and colour them.
func kindTrack(events.Kind) events.TrackID { return events.TrackRTP }

func trackTimescale(trackID uint32) uint32 {
	for _, t := range fixture.DefaultTracks() {
		if t.TrackID == trackID {
			return t.Timescale
		}
	}
	return 1000
}

// finishRun renders frames, synthesises audio, assembles the manifest and
// writes it into the confined output directory.
func finishRun(res RunResult, cfg Config, outDir string) (manifest.Manifest, Report, *events.Error) {
	tl := res.Timeline
	committed := tl.Committed()
	// Only unresolved gaps are reported: a gap filled by a late packet no
	// longer represents missing data.
	gaps := tl.Gaps(true)

	endNS := fixture.CorpusSpanNS
	if maxAt := tl.MaxEventAt(); maxAt > endNS {
		endNS = maxAt + cfg.LateWindowNS
	}
	cfg.RenderConfig.StartNS = 0
	cfg.AudioConfig.StartNS = 0
	cfg.AudioConfig.EndNS = endNS

	seq, rerr := render.RenderFrameSequence(cfg.RenderConfig, committed, endNS)
	if rerr != nil {
		return manifest.Manifest{}, res.Report, rerr
	}
	pcm, astats, aerr := audio.RenderSamples(cfg.AudioConfig, committed)
	if aerr != nil {
		return manifest.Manifest{}, res.Report, aerr
	}
	_ = pcm

	byTrack := map[string]int{}
	for _, e := range committed {
		byTrack[string(e.Track)]++
	}
	rtpSummary := manifest.RTPSummary{
		Received:   res.RTPStats.Received,
		Delivered:  res.RTPStats.Delivered,
		Duplicates: res.RTPStats.Duplicate,
		OutOfOrder: res.RTPStats.OutOfOrder,
		Lost:       res.RTPStats.Lost,
		Late:       res.RTPStats.Late,
		Status:     res.RTPStatus,
		Packets:    len(res.Archive.RTP),
	}
	inputs := manifest.Inputs{
		Fixture: string(cfg.Fixture),
		Seed:    cfg.Seed,
		Timeline: manifest.TimelineLike{
			WatermarkNS: tl.Watermark(),
			Committed:   committed,
			Pending:     len(tl.Pending()),
			ByTrack:     byTrack,
			Replays:     tl.Stats().Replays,
			Late:        tl.Stats().Late,
			Admitted:    tl.Stats().Admitted,
		},
		Frames:      seq,
		Audio:       astats,
		RTP:         rtpSummary,
		Gaps:        gaps,
		Errors:      res.Report.Errors,
		StateHash:   tl.StateHash(),
		Recovered:   res.Recovered,
		FrameWidth:  cfg.RenderConfig.Width,
		FrameHeight: cfg.RenderConfig.Height,
		SegmentSize: cfg.SegmentSize,
	}
	m := manifest.Build(inputs)
	body, err := m.Bytes()
	if err != nil {
		return manifest.Manifest{}, res.Report, events.NewError(events.CodeWrite, "", 0, -1, "manifest", err.Error(), err)
	}
	confDir, derr := manifest.ResolveOutDir(outDir)
	if derr != nil {
		return manifest.Manifest{}, res.Report, events.NewError(events.CodePath, "", 0, -1, "output", derr.Error(), derr)
	}
	path, werr := manifest.WriteAtomic(confDir, "manifest.json", body)
	if werr != nil {
		return manifest.Manifest{}, res.Report, events.NewError(events.CodeWrite, "", 0, -1, "write", werr.Error(), werr)
	}

	// Render frames were already hashed; the manifest reports the sequence
	// digest. For terminal output, also report frame and segment counts.
	segCount := 0
	if cfg.SegmentSize > 0 {
		segCount = (len(seq.Frames) + cfg.SegmentSize - 1) / cfg.SegmentSize
	} else {
		segCount = len(seq.Frames)
	}

	rep := res.Report
	rep.Frames = len(seq.Frames)
	rep.FrameDuration = seq.FrameDurationNS
	rep.MaxAlignErr = seq.MaxAlignError
	rep.FramesOK = seq.FrameDurationNS > 0 && seq.MaxAlignError <= seq.FrameDurationNS
	rep.MeanByte = m.Frames.MeanByte
	rep.MinByte = m.Frames.MinByte
	rep.MaxByte = m.Frames.MaxByte
	// Report the same rounded values the manifest publishes so terminal
	// output and JSON never disagree.
	rep.Peak = m.Audio.Peak
	rep.PeakDBFS = m.Audio.PeakDBFS
	rep.AudioFinite = audio.PeakWithin(astats)
	rep.StateHash = tl.StateHash()
	rep.FramesHash = seq.SequenceHash()
	rep.ManifestHash = m.ManifestSHA256
	rep.Segments = segCount
	rep.GapCount = len(gaps)
	rep.ErrorCount = len(res.Report.Errors)
	rep.Committed = len(committed)
	rep.Replays = tl.Stats().Replays
	rep.Late = tl.Stats().Late
	rep.WatermarkNS = tl.Watermark()
	rep.Recovered = res.Recovered
	rep.RTPSummary = rtpSummary
	rep.ManifestPath = path

	res.Manifest = m
	res.Frames = seq
	res.Audio = astats
	return m, rep, nil
}

// SortedErrorLines renders reported errors for terminal output.
func SortedErrorLines(errs []*events.Error) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		if e == nil {
			continue
		}
		out = append(out, e.Error())
	}
	sort.Strings(out)
	return out
}
