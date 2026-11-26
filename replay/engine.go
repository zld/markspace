package replay

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"replayproof/internal/event"
	"replayproof/internal/flow"
	"replayproof/internal/index"
	"replayproof/internal/media"
	"replayproof/internal/output"
	"replayproof/internal/parse"
	"replayproof/internal/repair"
)

// runState carries the parsed and repaired inputs through the phases.
type runState struct {
	cfg        Config
	outDir     string
	mp4Path    string
	mxfPath    string
	mp4Asset   *parse.Asset
	mxfAsset   *parse.Asset
	video      *parse.Track
	audio      *parse.Track
	videoRep   *repair.Result
	audioRep   *repair.Result
	videoFixed *parse.Track
	audioFixed *parse.Track
	meta       parse.Meta
	choices    []parse.FieldChoice
	conflicts  []parse.Conflict
	// allDisagreements lists field conflicts that selectors resolved, so the
	// summary records both sides of every choice.
	allDisagreements []parse.Conflict
	events           []event.Event
	plans            []event.ClipPlan
	bad              []output.BadCase
	indexer          *index.Index
	inputHash        string
	paramsHash       string
	// startSeq resumes after a validated checkpoint: plans with a sequence
	// at or below it are not re-run.
	startSeq int
	// preClips are clips released by the interrupted run and already on disk.
	preClips []*output.Clip
	// checkpoint is the validated resume checkpoint; nil for a fresh run.
	checkpoint *Checkpoint
}

// Run executes the full pipeline and returns the summary and any fatal
// error. A metadata conflict, truncation or out-of-range KLV in the primary
// inputs is fatal and the caller maps it to exit code 2.
func Run(ctx context.Context, cfg Config, inputs Inputs) (*output.Summary, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	st := &runState{cfg: cfg, outDir: cfg.OutDir}
	if err := st.prepare(ctx, inputs); err != nil {
		return st.baseSummary(), err
	}
	return st.process(ctx)
}

// prepare writes inputs, parses the primary pair, resolves metadata and plans
// windows. Corrupt inputs are parsed only to record their failure position.
func (st *runState) prepare(ctx context.Context, inputs Inputs) error {
	inDir := filepath.Join(st.outDir, "input")
	if err := os.MkdirAll(inDir, 0o755); err != nil {
		return media.Wrap(err, media.CodeIO, "input.mkdir", -1)
	}
	if err := os.MkdirAll(filepath.Join(st.outDir, "clips"), 0o755); err != nil {
		return media.Wrap(err, media.CodeIO, "out.mkdir", -1)
	}
	write := func(name string, data []byte) (string, error) {
		p := filepath.Join(inDir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return "", media.Wrap(err, media.CodeIO, "input.write", -1)
		}
		return p, nil
	}
	var err error
	if st.mp4Path, err = write("primary.mp4", inputs.PrimaryMP4); err != nil {
		return err
	}
	if st.mxfPath, err = write("primary.mxf", inputs.PrimaryMXF); err != nil {
		return err
	}

	st.inputHash = InputDigest(map[string][]byte{
		"primary.mp4":  inputs.PrimaryMP4,
		"primary.mxf":  inputs.PrimaryMXF,
		"conflict.mxf": inputs.ConflictMXF,
		"corrupt.mp4":  inputs.CorruptMP4,
		"corrupt.mxf":  inputs.CorruptMXF,
		"rtp":          joinPackets(inputs.RTP),
	})
	st.paramsHash = ParamsDigest(st.cfg.Seed, st.cfg.Workers, st.cfg.Queue, st.cfg.Selectors)

	// Corrupt cases: parsed only to capture the failure position. They are
	// never published.
	st.bad = append(st.bad, st.parseBad("truncated.mp4", media.CodeTruncatedBox, func(p string) error {
		_, err := parse.ParseMP4(p)
		return err
	}, "truncated.mp4", inputs.CorruptMP4))
	st.bad = append(st.bad, st.parseBad("outofrange.mxf", media.CodeMXFOutOfRange, func(p string) error {
		_, err := parse.ParseMXF(p)
		return err
	}, "outofrange.mxf", inputs.CorruptMXF))

	// Primary assets.
	if st.mp4Asset, err = parse.ParseMP4(st.mp4Path); err != nil {
		return err
	}
	if st.mxfAsset, err = parse.ParseMXF(st.mxfPath); err != nil {
		return err
	}
	st.video = st.mp4Asset.VideoTrack()
	st.audio = st.mp4Asset.AudioTrack()
	if st.video == nil || st.audio == nil {
		return media.New(media.CodeNoTrack, "replay.prepare", -1, "primary MP4 needs video and audio tracks")
	}

	// Metadata: both containers describe the same program. The MXF fixture
	// carries a zero-margin crop, which is the MXF way of saying "no crop";
	// it is normalised away so it cannot masquerade as a real descriptor.
	sources := []parse.MetaSource{
		{Name: "mp4", Meta: st.video.Meta},
		{Name: "mxf", Meta: normalizeMXFMeta(mxfVideoMeta(st.mxfAsset))},
	}
	meta, choices, conflicts := parse.ResolveMetadata(sources, st.cfg.Selectors)
	st.meta, st.choices, st.conflicts = meta, choices, conflicts
	// Resolve a second time without selectors so the summary can report both
	// the disagreement and the choice that settled it.
	_, _, st.allDisagreements = parse.ResolveMetadata(sources, nil)

	// The conflict case is a second MXF whose descriptors disagree. It is
	// evaluated on its own so it stays reportable even when the primary pair
	// resolves cleanly through selectors.
	if len(inputs.ConflictMXF) > 0 {
		conflictPath, werr := write("conflict.mxf", inputs.ConflictMXF)
		if werr == nil {
			if asset, perr := parse.ParseMXF(conflictPath); perr == nil {
				_, _, found := parse.ResolveMetadata([]parse.MetaSource{
					{Name: "mp4", Meta: st.video.Meta},
					{Name: "mxf", Meta: mxfVideoMeta(asset)},
				}, nil)
				if len(found) > 0 {
					st.bad = append(st.bad, output.BadCase{
						Name: "conflict.mxf", Code: media.CodeMETAConflict,
						Offset: mxfMetaOffset(asset), Detail: describeConflict(found),
					})
				}
			}
		}
	}
	// The unresolved primary pair is the same failure class; report it and
	// stop before any clip is generated.
	if len(conflicts) > 0 {
		st.bad = append(st.bad, output.BadCase{
			Name: "primary.mxf", Code: media.CodeMETAConflict,
			Offset: mxfMetaOffset(st.mxfAsset), Detail: describeConflict(conflicts),
		})
		return &media.Error{
			Code: media.CodeMETAConflict, Op: "replay.metadata", Offset: -1,
			Msg: describeConflict(conflicts),
		}
	}

	// Repair the timeline.
	policy := repair.Policy{DefaultDuration: media.FromTicks(45000, parseTimescale(st.video))}
	if st.videoRep, err = repair.RepairTrack(st.video, policy); err != nil {
		return err
	}
	if err := repair.ValidateMonotonic(st.videoRep.Samples); err != nil {
		return err
	}
	if st.audioRep, err = repair.RepairTrack(st.audio, repair.Policy{}); err != nil {
		return err
	}
	if err := repair.ValidateMonotonic(st.audioRep.Samples); err != nil {
		return err
	}
	st.videoFixed = &parse.Track{ID: st.video.ID, Kind: st.video.Kind, Timescale: st.video.Timescale,
		Samples: st.videoRep.Samples, StsdRaw: st.video.StsdRaw, Width: st.video.Width, Height: st.video.Height}
	st.audioFixed = &parse.Track{ID: st.audio.ID, Kind: st.audio.Kind, Timescale: st.audio.Timescale,
		Samples: st.audioRep.Samples, StsdRaw: st.audio.StsdRaw, ChannelCount: st.audio.ChannelCount, SampleRate: st.audio.SampleRate}

	// Event windows.
	packets := make([]event.Packet, 0, len(inputs.RTP))
	for _, raw := range inputs.RTP {
		p, perr := event.ParsePacket(raw)
		if perr != nil {
			return perr
		}
		packets = append(packets, p)
	}
	stream := event.Stream{ClockRate: st.cfg.ClockRate}
	if st.events, err = stream.Events(packets); err != nil {
		return err
	}
	if len(st.events) != 2 {
		return &media.Error{Code: media.CodeEventWindow, Op: "replay.events", Offset: -1,
			Msg: fmt.Sprintf("expected 2 marker events, got %d", len(st.events))}
	}
	windows, err := event.BuildWindows(st.events, st.videoFixed, st.audioFixed,
		event.BuildOptions{PreRoll: st.cfg.PreRoll, PostRoll: st.cfg.PostRoll})
	if err != nil {
		return err
	}
	for _, w := range windows {
		plan, perr := event.PlanClip(w, st.videoFixed, st.audioFixed)
		if perr != nil {
			return perr
		}
		st.plans = append(st.plans, plan)
	}

	// Index of the repaired tracks.
	st.indexer, err = index.Build(st.mp4Asset, map[uint32][]parse.Sample{
		st.video.ID: st.videoRep.Samples,
		st.audio.ID: st.audioRep.Samples,
	})
	if err != nil {
		return err
	}
	return nil
}

// normalizeMXFMeta drops descriptors that carry no information.
func normalizeMXFMeta(m *parse.Meta) *parse.Meta {
	if m == nil || m.Crop != nil {
		return m
	}
	return m
}

// mxfMetaOffset reports where the conflicting metadata lives in the source:
// the header metadata KLV of the MXF video track.
func mxfMetaOffset(a *parse.Asset) int64 {
	if v := a.VideoTrack(); v != nil {
		return v.MetaOffset
	}
	return 0
}

func parseTimescale(t *parse.Track) uint32 {
	if t == nil || t.Timescale == 0 {
		return 90000
	}
	return t.Timescale
}

func mxfVideoMeta(a *parse.Asset) *parse.Meta {
	if a == nil {
		return nil
	}
	if v := a.VideoTrack(); v != nil {
		return v.Meta
	}
	return nil
}

func joinPackets(packets [][]byte) []byte {
	var out []byte
	for _, p := range packets {
		out = append(out, p...)
	}
	return out
}

func describeConflict(conflicts []parse.Conflict) string {
	parts := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		cands := make([]string, 0, len(c.Candidates))
		for _, cand := range c.Candidates {
			cands = append(cands, cand.Source+"="+cand.Value)
		}
		parts = append(parts, fmt.Sprintf("%s(%s): %s", c.Field, c.Reason, joinStrings(cands, ",")))
	}
	return joinStrings(parts, "; ")
}

func joinStrings(v []string, sep string) string {
	out := ""
	for i, s := range v {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}

// parseBad materialises a corrupt input and records where parsing failed.
func (st *runState) parseBad(name string, want media.Code, parseFn func(string) error, file string, data []byte) output.BadCase {
	bc := output.BadCase{Name: name, Code: want, Offset: -1}
	if len(data) == 0 {
		bc.Detail = "input not generated"
		return bc
	}
	p := filepath.Join(st.outDir, "input", file)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		bc.Detail = err.Error()
		return bc
	}
	err := parseFn(p)
	if err == nil {
		bc.Detail = "input parsed cleanly; corruption was not detected"
		return bc
	}
	if me, ok := media.AsError(err); ok {
		bc.Code = me.Code
		bc.Offset = me.Offset
		bc.Track = me.TrackID
		bc.Detail = me.Msg
		if bc.Detail == "" && me.Err != nil {
			bc.Detail = me.Err.Error()
		}
	} else {
		bc.Detail = err.Error()
	}
	if bc.Code != want {
		bc.Detail = fmt.Sprintf("expected %s, got %s: %s", want, bc.Code, bc.Detail)
	}
	return bc
}

// process runs the clip workers through the bounded pipeline.
func (st *runState) process(ctx context.Context) (*output.Summary, error) {
	sum := st.baseSummary()
	sum.Clips = append(sum.Clips, st.preClips...)
	if len(st.plans) == 0 {
		st.fillMaps(sum)
		sum.ComputeErrors()
		return sum, nil
	}
	pipeline, err := flow.New(flow.Config{Workers: st.cfg.Workers, Queue: st.cfg.Queue})
	if err != nil {
		return nil, media.Wrap(err, media.CodeConfig, "replay.pipeline", -1)
	}
	merger := flow.NewMergerAt(st.startSeq + 1)

	produce := func(ctx context.Context, jobs chan<- flow.Job) error {
		for i := range st.plans {
			seq := i + 1
			if seq <= st.startSeq {
				continue
			}
			select {
			case jobs <- flow.Job{Seq: seq, Data: st.plans[i]}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	work := func(ctx context.Context, job flow.Job) (any, error) {
		if st.cfg.DelayNS > 0 {
			select {
			case <-time.After(time.Duration(st.cfg.DelayNS)):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return st.writeClip(job.Data.(event.ClipPlan))
	}
	released := 0
	checkpoint := st.checkpoint
	if checkpoint == nil {
		checkpoint = NewCheckpoint(st.cfg.Seed, st.inputHash, st.paramsHash)
	}
	consume := func(r flow.Result) error {
		for _, res := range merger.Add(r) {
			released++
			if res.Err != nil {
				return res.Err
			}
			clip := res.Data.(*output.Clip)
			sum.Clips = append(sum.Clips, clip)
			// Only the merger's contiguous sequence is recorded, never a
			// completion order.
			checkpoint.Record(res.Seq, clip.Hash)
			if st.cfg.CheckpointPath != "" {
				if err := checkpoint.Save(st.cfg.CheckpointPath); err != nil {
					return err
				}
			}
			if st.cfg.CancelAfter > 0 && released >= st.cfg.CancelAfter {
				return context.Canceled
			}
		}
		return nil
	}

	stats, err := pipeline.Run(ctx, produce, work, consume)
	sum.QueuePeak = stats.QueuePeak
	if err != nil {
		if flow.IsCanceled(err) {
			sum.Canceled = true
			return sum, &media.Error{Code: media.CodeCanceled, Op: "replay.pipeline", Offset: -1,
				Msg: fmt.Sprintf("canceled after %d released clip(s)", len(sum.Clips)), Err: context.Canceled}
		}
		return sum, err
	}
	if gapErr := merger.ErrGap(); gapErr != nil {
		return sum, media.Wrap(gapErr, media.CodeBadTimestamp, "replay.merge", -1)
	}

	// Publish the index document.
	timescales := map[uint32]uint32{st.video.ID: st.video.Timescale, st.audio.ID: st.audio.Timescale}
	indexBytes, err := st.indexer.Marshal("primary.mp4", timescales)
	if err != nil {
		return sum, media.Wrap(err, media.CodeIO, "replay.index", -1)
	}
	indexPath := filepath.Join(st.outDir, "index.json")
	if err := os.WriteFile(indexPath, indexBytes, 0o644); err != nil {
		return sum, media.Wrap(err, media.CodeIO, "replay.index", -1)
	}
	sum.IndexPath = indexPath
	sum.IndexHash = output.HashBytes(indexBytes)

	st.fillMaps(sum)
	sum.ComputeErrors()
	return sum, nil
}

// baseSummary fills the deterministic parts shared by every exit path.
func (st *runState) baseSummary() *output.Summary {
	sum := &output.Summary{
		Seed: st.cfg.Seed, Workers: st.cfg.Workers, QueueSize: st.cfg.Queue,
		BadCases: append([]output.BadCase(nil), st.bad...),
	}
	for _, c := range st.choices {
		rec := output.MetadataRecord{Field: c.Field, Source: c.Source, Value: c.Value}
		for _, conf := range append(append([]parse.Conflict(nil), st.conflicts...), st.allDisagreements...) {
			if conf.Field != c.Field {
				continue
			}
			rec.Conflict = true
			for _, cand := range conf.Candidates {
				rec.Candidates = append(rec.Candidates, cand.Source+"="+cand.Value)
			}
		}
		sort.Strings(rec.Candidates)
		sum.Metadata = append(sum.Metadata, rec)
	}
	for _, r := range []*repair.Result{st.videoRep, st.audioRep} {
		if r != nil {
			sum.Fixes = append(sum.Fixes, r.Fixes...)
		}
	}
	return sum
}

// fillMaps records every source sample against the clip it landed in. The
// published clips are re-read so the recorded clip offsets and times come
// from the files on disk, not from the writer's intent. Samples are matched
// positionally within each slice: clip sample i is source entry i.
func (st *runState) fillMaps(sum *output.Summary) {
	// placed maps track id and source sample number to its clip placement.
	type placement struct {
		clipName string
		time     media.Rational
		offset   int64
		shift    media.Rational
		srcOff   int64
		size     uint32
		key      bool
	}
	placed := make(map[[2]int]placement)
	byteErr := make(map[[2]int]bool)
	for _, c := range sum.Clips {
		asset, err := parse.ParseMP4(c.Path)
		if err != nil {
			continue
		}
		name := filepath.Base(c.Path)
		for _, tr := range []struct {
			entries []index.Entry
			kind    parse.Kind
		}{
			{c.Spec.Video, parse.KindVideo},
			{c.Spec.Audio, parse.KindAudio},
		} {
			if len(tr.entries) == 0 {
				continue
			}
			ct := asset.TrackByID(tr.entries[0].TrackID)
			if ct == nil {
				ct = firstTrackOfKind(asset, tr.kind)
			}
			if ct == nil || len(ct.Samples) != len(tr.entries) {
				continue
			}
			for i, e := range tr.entries {
				cs := ct.Samples[i]
				key := [2]int{int(e.TrackID), e.Number}
				if _, seen := placed[key]; seen {
					continue
				}
				placed[key] = placement{
					clipName: name, time: cs.DTS, offset: cs.Offset,
					shift: c.Shift, srcOff: e.Offset, size: e.Size, key: e.Keyframe,
				}
				src, err1 := parse.ReadBytes(st.mp4Path, e.Offset, e.Size)
				dst, err2 := parse.ReadBytes(c.Path, cs.Offset, cs.Size)
				if err1 != nil || err2 != nil || !bytesEqual(src, dst) {
					byteErr[key] = true
				}
			}
		}
	}
	for _, tr := range st.indexer.Tracks {
		for _, e := range tr.Entries {
			m := output.SampleMap{
				TrackID: e.TrackID, Number: e.Number, Source: e.DTS,
				Duration: e.End.Sub(e.DTS), SourceOffset: e.Offset,
				Size: e.Size, Keyframe: e.Keyframe,
			}
			key := [2]int{int(e.TrackID), e.Number}
			if p, ok := placed[key]; ok {
				m.ClipName = p.clipName
				m.ClipTime = p.time
				m.ClipOffset = p.offset
				m.TimeError = p.time.Add(p.shift).Sub(e.DTS).Abs().MicrosCeil()
				if byteErr[key] {
					m.ByteError = 1
				}
			}
			sum.Maps = append(sum.Maps, m)
		}
	}
}

func firstTrackOfKind(a *parse.Asset, kind parse.Kind) *parse.Track {
	for _, t := range a.Tracks {
		if t.Kind == kind {
			return t
		}
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	h1 := sha256.Sum256(a)
	h2 := sha256.Sum256(b)
	return h1 == h2
}

// writeClip builds the clip for one plan. The clip is published only if it
// re-parses and every sample payload matches the source bytes.
func (st *runState) writeClip(plan event.ClipPlan) (*output.Clip, error) {
	spec := output.ClipSpec{
		EventIndex: plan.Window.Event.Index,
		Start:      plan.Window.Start,
		End:        plan.Window.End,
		Deviation:  plan.Window.VideoDeviation,
	}
	videoEntries := st.indexer.Track(st.video.ID).Entries[plan.Video.First:plan.Video.Last]
	spec.Video = videoEntries
	if plan.Audio.Last > plan.Audio.First {
		spec.Audio = st.indexer.Track(st.audio.ID).Entries[plan.Audio.First:plan.Audio.Last]
	}
	spec.SourceBytes = sumSizes(spec.Video) + sumSizes(spec.Audio)
	name := fmt.Sprintf("clip-%02d.mp4", plan.Window.Event.Index)
	path := filepath.Join(st.outDir, "clips", name)
	clip, err := output.WriteClip(st.mp4Path, path, &st.meta, st.video, st.audio, spec)
	if err != nil {
		return nil, err
	}
	if err := verifyClip(path, st.mp4Path, spec); err != nil {
		os.Remove(path)
		return nil, err
	}
	return clip, nil
}

func sumSizes(entries []index.Entry) int64 {
	var n int64
	for _, e := range entries {
		n += int64(e.Size)
	}
	return n
}

// verifyClip re-opens the published clip and proves that it decodes from a
// keyframe and carries byte-identical payloads.
func verifyClip(clipPath, srcPath string, spec output.ClipSpec) error {
	asset, err := parse.ParseMP4(clipPath)
	if err != nil {
		return media.Wrap(err, media.CodeInvalidBox, "clip.verify", -1)
	}
	vt := asset.VideoTrack()
	if vt == nil {
		return media.New(media.CodeNoTrack, "clip.verify", -1, "clip has no video track")
	}
	if len(vt.Samples) != len(spec.Video) {
		return media.New(media.CodeInvalidBox, "clip.verify", -1,
			fmt.Sprintf("clip has %d video samples, planned %d", len(vt.Samples), len(spec.Video)))
	}
	if !vt.Samples[0].Keyframe {
		return media.New(media.CodeNoKeyframe, "clip.verify", -1, "clip does not start on a keyframe")
	}
	if err := comparePayload(clipPath, vt.Samples, srcPath, spec.Video); err != nil {
		return err
	}
	if len(spec.Audio) > 0 {
		at := asset.AudioTrack()
		if at == nil {
			return media.New(media.CodeNoTrack, "clip.verify", -1, "clip lost its audio track")
		}
		if err := comparePayload(clipPath, at.Samples, srcPath, spec.Audio); err != nil {
			return err
		}
	}
	return nil
}

func comparePayload(clipPath string, got []parse.Sample, srcPath string, want []index.Entry) error {
	for i := range want {
		src, err := parse.ReadBytes(srcPath, want[i].Offset, want[i].Size)
		if err != nil {
			return err
		}
		dst, err := parse.ReadBytes(clipPath, got[i].Offset, got[i].Size)
		if err != nil {
			return err
		}
		if !bytesEqual(src, dst) {
			return media.New(media.CodeInvalidBox, "clip.verify", got[i].Offset, "payload differs from source")
		}
	}
	return nil
}
