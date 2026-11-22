// Package app is the CLI adapter: it generates fixtures, drives the library
// subsystems in their data-flow order and prints the terminal summary. It
// contains no DSP or queue logic of its own.
package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/pion/rtp"

	"medpipe/audio"
	"medpipe/fixture"
	"medpipe/manifest"
	"medpipe/stream"
	"medpipe/timeline"
)

// Options is the parsed CLI configuration for one demo run.
type Options struct {
	Seed     int64
	Rate     uint32
	Channels int
	Queue    int
	SlowMS   int
	OutDir   string
	Fixture  string
}

// Summary is the terminal output and process exit code of a run.
type Summary struct {
	Manifest   manifest.Manifest
	ExitCode   int
	ErrorStage string
	TrackID    string
	Checkpoint timeline.Checkpoint
}

type job struct {
	desc fixture.Descriptor
	pcm  []byte
	bits uint16
}

// Run executes the full pipeline: fixtures -> timeline/graph -> audio
// pipeline -> bounded queue -> manifest.
func Run(ctx context.Context, opt Options) (Summary, error) {
	layout := layoutFor(opt.Channels)
	outRate := opt.Rate
	if outRate == 0 {
		outRate = 48000
	}
	spec := fixture.Spec{
		Seed: opt.Seed, Rate: outRate, Channels: opt.Channels, Layout: layout,
		OutDir: filepath.Join(opt.OutDir, "fixtures"), Fault: opt.Fixture,
	}
	set, err := fixture.Generate(spec)
	if err != nil {
		return fail(timeline.StageWAV, "", timeline.Checkpoint{}, err), nil
	}
	// 1. Dependency graph: cycles and unknown upstreams are caught before any
	// sample is processed.
	graph, err := timeline.BuildGraph(set.Tracks)
	if err != nil {
		return fail(timeline.StageTimeline, "", timeline.Checkpoint{}, err), nil
	}
	order, err := graph.TopoSort()
	if err != nil {
		return fail(timeline.StageTimeline, "", timeline.Checkpoint{}, err), nil
	}
	clock, _ := timeline.NewClock(outRate)
	store := stream.NewMemCheckpoint()
	builder := manifest.NewBuilder(clock, opt.Seed, outRate, opt.Channels, opt.Queue)

	// 2. Track validation and upstream gap detection. Missing blocks on an
	// upstream track fail its downstreams while checkpoints stay committed.
	if fe := validateSet(set, order, graph); fe != nil {
		cp, _ := store.Load(fe.TrackID)
		builder.AddFailure(manifest.FailureRecord{
			Code: fe.Code, Stage: fe.Stage, TrackID: fe.TrackID,
			Message: fe.Msg, Checkpoint: cp,
		})
		m := builder.Build()
		_ = writeArtifacts(opt.OutDir, m)
		return Summary{Manifest: m, ExitCode: 2, ErrorStage: fe.Stage,
			TrackID: fe.TrackID, Checkpoint: cp}, nil
	}
	return runHealthy(ctx, opt, set, order, q(opt), store, builder, clock, layout, outRate)
}

func q(opt Options) *stream.BoundedQueue {
	q, err := stream.NewBoundedQueue(opt.Queue)
	if err != nil {
		panic(err) // validated by CLI flag parsing before Run
	}
	return q
}

func validateSet(set *fixture.Set, order []string, graph *timeline.Graph) *timeline.Error {
	for _, id := range order {
		tr := set.Track(id)
		if err := tr.Validate(); err != nil {
			return locate(err)
		}
		for _, up := range graph.Upstreams(id) {
			if gaps := set.Track(up).Gaps(); len(gaps) > 0 {
				g := gaps[0]
				return timeline.NewError(timeline.ErrCodeMissingBlock, timeline.StageTimeline, id,
					"upstream %q missing %d samples in [%d,%d)", up, g.Length(), g.Start, g.End)
			}
		}
	}
	return nil
}

func runHealthy(ctx context.Context, opt Options, set *fixture.Set, order []string,
	qu *stream.BoundedQueue, store *stream.MemCheckpoint, builder *manifest.Builder,
	clock timeline.Clock, layout string, outRate uint32) (Summary, error) {
	rtpValidators := map[string]*stream.RTPValidator{}
	pipes := map[string]*audio.Pipeline{}
	for _, id := range order {
		rtpValidators[id] = stream.NewRTPValidator(id)
		p, perr := audio.NewPipeline(id, set.Spec.Rate, outRate, layout, audio.LayoutStereo, -23)
		if perr != nil {
			return fail(timeline.StageResample, id, timeline.Checkpoint{}, perr), nil
		}
		pipes[id] = p
	}
	proc := func(ctx context.Context, b stream.QueuedBlock) (timeline.Interval, error) {
		if opt.SlowMS > 0 {
			select {
			case <-ctx.Done():
				return timeline.Interval{}, ctx.Err()
			case <-time.After(time.Duration(opt.SlowMS) * time.Millisecond):
			}
		}
		j := b.Payload.(*job)
		hdr, _, err := audio.ParseWAV(bytes.NewReader(j.pcm))
		if err != nil {
			return timeline.Interval{}, err
		}
		// Extract the PCM payload from the validated container for the DSP chain.
		offset, err := audio.WAVPayloadOffset(j.pcm)
		if err != nil {
			return timeline.Interval{}, err
		}
		pcm := j.pcm[offset:]
		res, err := pipes[b.TrackID].ProcessBlock(pcm, hdr.BitsPerSample, b.Start)
		if err != nil {
			return timeline.Interval{}, err
		}
		return res.Interval, nil
	}
	r, err := stream.NewRunner(qu, store, proc, 1)
	if err != nil {
		return Summary{}, err
	}
	producer := func(ctx context.Context) error {
		for _, id := range order {
			for _, d := range set.FilesOf(id) {
				raw, rerr := os.ReadFile(d.Path)
				if rerr != nil {
					return timeline.NewError(timeline.ErrCodeTruncatedWAV, timeline.StageWAV, id,
						"read %s: %v", d.Path, rerr)
				}
				// RTP sequence/timestamp validation happens before enqueue so
				// jumps are reported at the rtp stage with the track id.
				if d.RTPSequence != timeline.RTPSeqAbsent {
					pkt := &rtp.Packet{Header: rtp.Header{
						SequenceNumber: d.RTPSequence, Timestamp: uint32(d.StartSample),
					}}
					if verr := rtpValidators[id].Observe(pkt); verr != nil {
						return verr
					}
				}
				if perr := qu.Push(ctx, stream.QueuedBlock{
					TrackID: d.TrackID, Sequence: d.Sequence, Start: d.StartSample,
					Payload: &job{desc: d, pcm: raw, bits: set.Spec.Bits},
				}); perr != nil {
					return perr
				}
			}
		}
		return nil
	}
	r.StartProducers(ctx, producer)
	r.StartConsumers(ctx)
	werr := r.Wait()

	for _, id := range order {
		pipe := pipes[id]
		if rec, ok := buildRecord(id, pipe, clock, outRate, opt.Channels); ok {
			if addErr := builder.AddTrack(rec); addErr != nil && werr == nil {
				werr = addErr
			}
		}
	}
	builder.SetQueueStats(r.QueueStats().PeakDepth)
	if werr != nil {
		pe, _ := timeline.AsError(werr)
		cp, _ := store.Load(pe.TrackID)
		builder.AddFailure(manifest.FailureRecord{
			Code: pe.Code, Stage: pe.Stage, TrackID: pe.TrackID,
			Message: pe.Msg, Checkpoint: cp,
		})
		m := builder.Build()
		_ = writeArtifacts(opt.OutDir, m)
		return Summary{Manifest: m, ExitCode: 2, ErrorStage: pe.Stage,
			TrackID: pe.TrackID, Checkpoint: cp}, nil
	}
	m := builder.Build()
	if err := writeArtifacts(opt.OutDir, m); err != nil {
		return fail(timeline.StageManifest, "", timeline.Checkpoint{}, err), nil
	}
	return Summary{Manifest: m, ExitCode: 0}, nil
}

func fail(stage, trackID string, cp timeline.Checkpoint, err error) Summary {
	pe, ok := timeline.AsError(err)
	if !ok {
		pe = timeline.NewError(timeline.ErrCodeCheckpoint, stage, trackID, "%v", err)
	}
	return Summary{ExitCode: 1, ErrorStage: pe.Stage, TrackID: pe.TrackID, Checkpoint: cp}
}

func locate(err error) *timeline.Error {
	pe, ok := timeline.AsError(err)
	if !ok {
		return timeline.NewError(timeline.ErrCodeCheckpoint, timeline.StageTimeline, "", "%v", err)
	}
	return pe
}

func layoutFor(channels int) string {
	switch channels {
	case 1:
		return audio.LayoutMono
	case 2:
		return audio.LayoutStereo
	case 6:
		return audio.Layout51
	case 8:
		return audio.Layout71
	default:
		return audio.LayoutStereo
	}
}
