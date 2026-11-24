package pipeline

import (
	"bytes"
	"encoding/gob"
	"os"
	"path/filepath"

	"mediapipeline/internal/checkpoint"
	"mediapipeline/internal/clock"
	mpierr "mediapipeline/internal/errors"
	"mediapipeline/internal/fixture"
	"mediapipeline/internal/jitter"
	"mediapipeline/internal/output"
	"mediapipeline/internal/provenance"
	"mediapipeline/internal/recovery"
	mediartp "mediapipeline/internal/rtp"
	"mediapipeline/internal/sync"
)

// audioArtifact is the materialized, resumable result of the recover node.
type audioArtifact struct {
	Samples   []output.Sample
	Gaps      []recovery.GapRecord
	GapCount  int
	Duplicate int
}

// videoArtifact is the materialized result of the sync node's video mapping.
type videoArtifact struct {
	Samples []output.Sample
}

func (e *Engine) artifactPath(name string) string {
	return filepath.Join(e.cfg.WorkDir, name+".gob")
}

func writeGob(path string, v any) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readGob(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return gob.NewDecoder(bytes.NewReader(raw)).Decode(v)
}

// runParse reads the full generated audio stream through the verifying parser.
func (e *Engine) runParse(name string) error {
	if err := e.failIfInterrupted(name); err != nil {
		return err
	}
	spec := fixture.Spec{Seed: e.cfg.Seed, Packets: demoPackets,
		SampleRate: fixture.OpusRate, StartSeq: 100}
	raw, _, err := fixture.GenerateAudio(spec)
	if err != nil {
		return mpierr.New(mpierr.CatUnknown, 0, name, "fixture generation failed").Wrap(err)
	}
	rd := mediartp.NewReader(bytes.NewReader(raw), fixture.OpusRate)
	var frames []*mediartp.Frame
	for {
		f, rerr := rd.Read()
		if rerr != nil {
			if rerr.Error() == "EOF" || isEOF(rerr) {
				break
			}
			return rerr
		}
		frames = append(frames, f)
	}
	art := struct {
		Frames     []*mediartp.Frame
		Duplicates int
	}{frames, rd.Duplicates()}
	if err := writeGob(e.artifactPath(name), art); err != nil {
		return mpierr.New(mpierr.CatCheckpoint, 0, name, "write artifact").Wrap(err)
	}
	e.rec.Add(provenance.Entry{
		Node: name, ConfigDigest: provenance.DigestBytes([]byte(e.configKey())),
		InputDigest: provenance.DigestBytes(raw), CheckpointState: "running",
	})
	return e.markDone(name, provenance.DigestBytes(raw), e.configKey())
}

// runJitter reorders frames inside the bounded window and records gaps/late.
func (e *Engine) runJitter(name string) error {
	if err := e.failIfInterrupted(name); err != nil {
		return err
	}
	var parsed struct {
		Frames     []*mediartp.Frame
		Duplicates int
	}
	if err := readGob(e.artifactPath(NodeParse), &parsed); err != nil {
		return mpierr.New(mpierr.CatCheckpoint, 0, name, "missing parse artifact").Wrap(err)
	}
	buf, err := jitter.New(jitter.Config{Window: e.cfg.JitterWindow, Node: name})
	if err != nil {
		return err
	}
	var late []*mpierr.MediaError
	for _, f := range parsed.Frames {
		if f.Duplicate {
			continue // duplicates are counted upstream and never enter the buffer
		}
		if perr := buf.Push(f); perr != nil {
			if me, ok := perr.(*mpierr.MediaError); ok && me.Category == mpierr.CatLate {
				late = append(late, me) // late packets are recorded, not fatal
				continue
			}
			return perr
		}
	}
	buf.Drain()
	var events []jitter.Event
	for {
		ev, ok := buf.Release()
		if !ok {
			break
		}
		events = append(events, ev)
	}
	art := struct {
		Events     []jitter.Event
		Late       []*mpierr.MediaError
		Duplicates int
	}{events, late, parsed.Duplicates}
	if err := writeGob(e.artifactPath(name), art); err != nil {
		return mpierr.New(mpierr.CatCheckpoint, 0, name, "write artifact").Wrap(err)
	}
	e.rec.Add(provenance.Entry{
		Node: name, ConfigDigest: provenance.DigestBytes([]byte(e.configKey())),
		InputDigest: e.inputDigestOf(NodeParse), CheckpointState: "running",
	})
	return e.markDone(name, e.inputDigestOf(NodeParse), e.configKey())
}

// runRecover maps events onto the shared timeline and reserves gap space.
func (e *Engine) runRecover(name string) error {
	if err := e.failIfInterrupted(name); err != nil {
		return err
	}
	var jit struct {
		Events     []jitter.Event
		Late       []*mpierr.MediaError
		Duplicates int
	}
	if err := readGob(e.artifactPath(NodeJitter), &jit); err != nil {
		return mpierr.New(mpierr.CatCheckpoint, 0, name, "missing jitter artifact").Wrap(err)
	}
	tl := clock.NewTimeline(clock.DefaultTickRateHz)
	policy := recovery.PolicySkipGaps
	if e.cfg.StrictRecovery {
		policy = recovery.PolicyStrict
	}
	rec, err := recovery.New(recovery.Config{
		Codec: recovery.CodecOpus, Policy: policy, Node: name,
		SampleRate: fixture.OpusRate,
	}, tl)
	if err != nil {
		return err
	}
	out := audioArtifact{Duplicate: jit.Duplicates}
	for _, ev := range jit.Events {
		res, herr := rec.Handle(ev)
		if herr != nil {
			return herr
		}
		if res.Frame != nil {
			out.Samples = append(out.Samples, output.Sample{
				Data: res.Frame.Payload, Ticks: uint32(res.Interval.Len())})
		} else {
			// Gap: explicit silence, never reconstructed codec content.
			out.Samples = append(out.Samples, output.Sample{
				Data: bytes.Repeat([]byte{0}, gapSilenceBytes), Ticks: uint32(res.Interval.Len())})
		}
		e.store.AppendInterval("audio", checkpoint.Interval{
			Start: res.Interval.Start, End: res.Interval.End})
	}
	out.Gaps = rec.Gaps()
	out.GapCount = len(out.Gaps)
	if err := writeGob(e.artifactPath(name), out); err != nil {
		return mpierr.New(mpierr.CatCheckpoint, 0, name, "write artifact").Wrap(err)
	}
	if err := e.store.Save(); err != nil {
		return mpierr.New(mpierr.CatCheckpoint, 0, name, "save intervals").Wrap(err)
	}
	e.rec.Add(provenance.Entry{
		Node: name, ConfigDigest: provenance.DigestBytes([]byte(e.configKey())),
		InputDigest: e.inputDigestOf(NodeJitter), CheckpointState: "running",
	})
	return e.markDone(name, e.inputDigestOf(NodeJitter), e.configKey())
}

// runSync maps video onto the timeline and validates the A/V offset.
func (e *Engine) runSync(name string) error {
	if err := e.failIfInterrupted(name); err != nil {
		return err
	}
	videoRaw, _, err := fixture.GenerateVideo(e.cfg.Seed, demoVideoFrames, 200)
	if err != nil {
		return mpierr.New(mpierr.CatUnknown, 0, name, "video fixture failed").Wrap(err)
	}
	tl := clock.NewTimeline(clock.DefaultTickRateHz)
	rd := mediartp.NewReader(bytes.NewReader(videoRaw), fixture.VideoRate)
	vart := videoArtifact{}
	var firstVideoStart uint64
	first := true
	for {
		f, rerr := rd.Read()
		if isEOF(rerr) {
			break
		}
		if rerr != nil {
			return rerr
		}
		ticks := tl.Convert(uint64(fixture.VideoStep), uint64(fixture.VideoRate))
		start := tl.Cursor(clock.KindVideo) + e.cfg.VideoDelayTicks
		iv, cerr := tl.Commit(clock.KindVideo, start, ticks)
		if cerr != nil {
			return mpierr.New(mpierr.CatSync, f.ExtSeq, name, "video commit failed").Wrap(cerr)
		}
		vart.Samples = append(vart.Samples, output.Sample{Data: f.Payload, Ticks: uint32(ticks)})
		if first {
			firstVideoStart = iv.Start
			first = false
		}
	}
	if err := writeGob(e.artifactPath(name), vart); err != nil {
		return mpierr.New(mpierr.CatCheckpoint, 0, name, "write artifact").Wrap(err)
	}
	aligner, err := sync.New(sync.Config{
		ToleranceTicks: e.cfg.SyncToleranceTicks, Node: name}, tl)
	if err != nil {
		return err
	}
	rep, aerr := aligner.Align(
		sync.TrackPoint{Kind: clock.KindAudio, Start: 0},
		sync.TrackPoint{Kind: clock.KindVideo, Start: firstVideoStart})
	// Record the measured offset before honoring the failure so the report
	// always carries it, even when mux is blocked.
	e.syncOffset = rep.OffsetTicks
	if aerr != nil {
		return aerr // CatSync blocks mux; report carries the measured offset
	}
	e.store.AppendInterval("video", checkpoint.Interval{Start: firstVideoStart,
		End: tl.Cursor(clock.KindVideo) + e.cfg.VideoDelayTicks})
	if err := e.store.Save(); err != nil {
		return mpierr.New(mpierr.CatCheckpoint, 0, name, "save intervals").Wrap(err)
	}
	e.rec.Add(provenance.Entry{
		Node: name, ConfigDigest: provenance.DigestBytes([]byte(e.configKey())),
		InputDigest: provenance.DigestBytes(videoRaw), CheckpointState: "running",
	})
	return e.markDone(name, provenance.DigestBytes(videoRaw), e.configKey())
}

// runMux writes the final MP4 from the persisted artifacts.
func (e *Engine) runMux(name string) error {
	if err := e.failIfInterrupted(name); err != nil {
		return err
	}
	var aa audioArtifact
	if err := readGob(e.artifactPath(NodeRecover), &aa); err != nil {
		return mpierr.New(mpierr.CatCheckpoint, 0, name, "missing audio artifact").Wrap(err)
	}
	var va videoArtifact
	if err := readGob(e.artifactPath(NodeSync), &va); err != nil {
		return mpierr.New(mpierr.CatCheckpoint, 0, name, "missing video artifact").Wrap(err)
	}
	audio := &output.Track{Kind: clock.KindAudio, TickRate: uint32(clock.DefaultTickRateHz),
		Timescale: uint32(clock.DefaultTickRateHz), Samples: aa.Samples}
	video := &output.Track{Kind: clock.KindVideo, TickRate: uint32(clock.DefaultTickRateHz),
		Timescale: uint32(fixture.VideoRate), Samples: va.Samples}
	sum, err := output.Write(e.cfg.OutPath, audio, video)
	if err != nil {
		return err
	}
	e.outputDigest = sum.SHA256
	e.rec.Add(provenance.Entry{
		Node: name, ConfigDigest: provenance.DigestBytes([]byte(e.configKey())),
		InputDigest: e.inputDigestOf(NodeRecover),
		OutputDigest: sum.SHA256, CheckpointState: "running",
	})
	return e.markDone(name, e.inputDigestOf(NodeRecover), e.configKey())
}
