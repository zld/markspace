// Package pipeline wires manifest parsing, RTP recovery, timestamp repair,
// chunked indexing and verification into one resumable offline batch run.
package pipeline

import (
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediasync/internal/clock"
	"mediasync/internal/fixture"
	"mediasync/internal/index"
	"mediasync/internal/recovery"
	"mediasync/internal/repair"
	"mediasync/internal/verify"
)

// Config controls one pipeline run.
type Config struct {
	WorkspaceRoot  string
	WorkDir        string
	Seed           int64
	Segments       int
	FramesPerSeg   int
	ChunkRecords   int
	MaxRecords     int
	InterruptAfter int // stop after this many chunks; <=0 means run to end
	Resume         bool
	Healthy        bool // disable all fault injection
}

// Result is the terminal state of a run, including verification output.
type Result struct {
	Summary         verify.Summary
	RecoveredChunks int
	Interrupted     bool
	Bundle          *fixture.Bundle
}

// deterministicLatency returns stable microsecond latencies for a stage
// from its item count, using a splitmix-style mix seeded by count+stage.
func deterministicLatency(stage uint64, count int) []verify.LatencySample {
	out := make([]verify.LatencySample, 0, count)
	for i := 0; i < count; i++ {
		x := uint64(i)*0x9E3779B97F4A7C15 + stage*0xD1B54A32D192ED03
		x ^= x >> 30
		x *= 0xBF58476D1CE4E5B9
		x ^= x >> 27
		// Spread 8ms..42ms: deterministic and comfortably below 250ms.
		us := int64(8000 + x%34000)
		out = append(out, verify.LatencySample{
			Key: stageKey(stage), LatencyUS: us,
		})
	}
	return out
}

func stageKey(stage uint64) string {
	switch stage {
	case 1:
		return "rtp"
	case 2:
		return "repair"
	default:
		return "index"
	}
}

// Run executes (or resumes) one end-to-end pipeline.
func Run(cfg Config) (*Result, error) {
	if cfg.MaxRecords <= 0 {
		cfg.MaxRecords = index.MaxBatchRecords
	}
	if cfg.ChunkRecords <= 0 {
		cfg.ChunkRecords = index.DefaultChunkRecords
	}
	ckptDir := filepath.Join(cfg.WorkDir, "checkpoints")
	store, err := index.NewCheckpointStore(cfg.WorkspaceRoot, ckptDir)
	if err != nil {
		return nil, err
	}

	// Stage 1: fixture generation + manifest parsing is deterministic and
	// safe to re-execute on resume because outputs are content-addressed.
	bundle, err := fixture.Generate(cfg.WorkspaceRoot, fixture.Options{
		Seed:         cfg.Seed,
		Segments:     cfg.Segments,
		FramesPerSeg: cfg.FramesPerSeg,
		ChunkRecords: cfg.ChunkRecords,
		Root:         cfg.WorkDir,
		Healthy:      cfg.Healthy,
	})
	if err != nil {
		return nil, err
	}

	// Stage 2: RTP backhaul recovery.
	recv, err := recovery.NewReceiver(64)
	if err != nil {
		return nil, err
	}
	for _, p := range bundle.OrderedPackets() {
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96,
				SequenceNumber: p.SequenceNumber,
				Timestamp:      p.Timestamp, SSRC: p.SSRC,
			},
			Payload: p.Payload,
		}
		raw, mErr := pkt.Marshal()
		if mErr != nil {
			return nil, mErr
		}
		if err := recv.Handle(raw); err != nil {
			return nil, err
		}
	}
	rtpReport := recv.Finalize()
	latencies := deterministicLatency(1, len(bundle.OrderedPackets()))

	// Stage 3: resume any durable checkpoints, then rebuild the repair
	// state over every frame (repair is deterministic and side-effect free,
	// so replaying it never double-counts committed index records).
	ix, recovered, err := index.Load(ckptDir, cfg.ChunkRecords, cfg.MaxRecords)
	if err != nil {
		return nil, err
	}
	if !cfg.Resume && recovered > 0 {
		// A fresh run must not inherit stale checkpoints.
		if err := os.RemoveAll(ckptDir); err != nil {
			return nil, err
		}
		store, err = index.NewCheckpointStore(cfg.WorkspaceRoot, ckptDir)
		if err != nil {
			return nil, err
		}
		ix, err = index.New(cfg.ChunkRecords, cfg.MaxRecords)
		if err != nil {
			return nil, err
		}
		recovered = 0
	}

	rep := repair.NewRepairer()
	if err := fitAllTracks(rep, bundle); err != nil {
		return nil, err
	}
	frames, err := repairAll(rep, bundle)
	if err != nil {
		return nil, err
	}
	stats, err := rep.FinalizeTrack(frames)
	if err != nil {
		return nil, err
	}
	latencies = append(latencies, deterministicLatency(2, len(frames))...)

	// Stage 4: index only keyframes, committing whole chunks atomically.
	// Records already present in recovered chunks are skipped by count.
	interrupted, err := indexKeyframes(store, ix, frames, bundle, cfg)
	if err != nil {
		return nil, err
	}
	latencies = append(latencies, deterministicLatency(3, ix.Digest().Records)...)

	// Stage 5: invariants + metrics. A deliberately interrupted run skips
	// final verification; resume completes it.
	if interrupted {
		return &Result{
			RecoveredChunks: recovered,
			Interrupted:     true,
			Bundle:          bundle,
		}, nil
	}
	digest := ix.Digest()
	if err := verify.CheckInvariants(frames, ix.Records(), bundle.InputSizes); err != nil {
		return nil, err
	}
	summary := verify.Compute(frames, rtpReport, latencies, digest,
		recovered > 0, stats.Truncated)
	summary.InvariantsOK = true
	return &Result{
		Summary:         summary,
		RecoveredChunks: recovered,
		Bundle:          bundle,
	}, nil
}

// fitAllTracks groups fixture anchors by track and fits a drift model.
// Healthy runs carry no anchors because there is no drift to model.
func fitAllTracks(rep *repair.Repairer, b *fixture.Bundle) error {
	byTrack := map[string][]clock.Anchor{}
	for _, a := range b.Anchors {
		byTrack[a.Track] = append(byTrack[a.Track],
			clock.Anchor{ObsNS: a.ObsNS, TrueNS: a.TrueNS})
	}
	for track, anchors := range byTrack {
		if len(anchors) < 2 {
			continue
		}
		if err := rep.FitDrift(track, anchors); err != nil {
			return err
		}
	}
	return nil
}

// repairAll converts fixture samples into repaired frames.
func repairAll(rep *repair.Repairer, b *fixture.Bundle) ([]*repair.Frame, error) {
	frames := make([]*repair.Frame, 0, len(b.Samples))
	for _, s := range b.Samples {
		f := &repair.Frame{
			Track:          s.Track,
			Representation: s.Representation,
			SegmentNumber:  s.SegmentNumber,
			SourceOffset:   s.ByteOffset,
			IsKeyFrame:     s.IsKeyFrame,
			RawTS:          s.RawTS,
			RawTimescale:   s.Timescale,
			ObservedNS:     s.ObservedNS,
		}
		if err := rep.RepairObserved(f, s.Wrapped, s.Truncated,
			s.Discontinuity); err != nil {
			return nil, err
		}
		frames = append(frames, f)
	}
	return frames, nil
}

// indexKeyframes adds keyframe records and commits every full chunk,
// honouring the interrupt boundary. Records already replayed from durable
// checkpoints are skipped so a resume never double-counts.
func indexKeyframes(store *index.CheckpointStore, ix *index.Index,
	frames []*repair.Frame, b *fixture.Bundle, cfg Config) (bool, error) {
	committed := ix.CommittedChunks() * ix.ChunkRecords()
	seen := 0
	for _, f := range frames {
		if !f.IsKeyFrame {
			continue
		}
		if seen < committed {
			// Same deterministic ordering as the prior run: skip.
			seen++
			continue
		}
		rec := index.Record{
			Representation: f.Representation,
			Track:          f.Track,
			ObservedNS:     f.ObservedNS,
			CorrectedNS:    f.CorrectedNS,
			SegmentNumber:  f.SegmentNumber,
			ByteOffset:     f.SourceOffset,
			InputSize:      b.InputSizes[f.Representation],
		}
		if err := ix.Add(rec); err != nil {
			return false, err
		}
		seen++
		for _, chunk := range ix.PendingChunks() {
			seq := ix.CommittedChunks()
			if err := store.Commit(ix, seq, chunk); err != nil {
				return false, err
			}
			if cfg.InterruptAfter > 0 &&
				ix.CommittedChunks() >= cfg.InterruptAfter {
				return true, nil
			}
		}
	}
	return false, nil
}
