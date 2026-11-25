// Package pipeline wires the processing stages together. A bounded channel
// carries access units from the scanner to the analysis worker: when the
// worker slows, the channel fills and the scanner blocks on send, so upstream
// reading stops. Context cancellation or any write failure makes every stage
// exit and the last committed segment is preserved in the checkpoint.
package pipeline

import (
	"context"

	"mediad/internal/checkpoint"
	"mediad/internal/clock"
	"mediad/internal/container"
	errs "mediad/internal/errors"
	"mediad/internal/fingerprint"
	"mediad/internal/fixture"
	"mediad/internal/index"
	"mediad/internal/segment"
	"mediad/internal/silence"
	"mediad/internal/stream"
)

// Status values for a pipeline invocation.
const (
	StatusCompleted   = "completed"
	StatusInterrupted = "interrupted"
)

// Options configures one run. QueueSize bounds the in-flight AU channel.
type Options struct {
	FixtureID      string
	Codec          stream.Codec
	Stream         []byte
	IndexText      []byte
	WorkDir        string
	QueueSize      int
	InterruptAfter int // stop after this many newly committed segments; 0 = never
	StreamCfg      stream.Config
}

// SegmentOutcome is the per-segment analysis result.
type SegmentOutcome struct {
	ID          uint64 `json:"id"`
	Hash        string `json:"sha256"`
	Corrupt     bool   `json:"corrupt"`
	RoundTripOK bool   `json:"roundtrip_ok"`
	Silence     string `json:"silence"`
}

// Result is the full run output consumed by the CLI acceptance command.
type Result struct {
	Status                string
	ResumedFrom           uint64
	Segments              []SegmentOutcome
	Match                 *index.Report
	Damages               []stream.Damage
	RoundTripRate         float64
	KeyframeHitRate       float64
	CorruptSegmentCount   int
	ManifestCanonicalHash string
	InputHash             string
	IndexHash             string
	CheckpointPath        string
	ManifestPath          string
}

// Run executes the full pipeline and returns its result.
func Run(parent context.Context, opts Options) (*Result, error) {
	if opts.QueueSize <= 0 {
		opts.QueueSize = 4
	}
	inputHash := fixture.HashBytes(opts.Stream)
	indexHash := fixture.HashBytes(opts.IndexText)
	cpPath := opts.WorkDir + "/checkpoint.json"
	manifestPath := opts.WorkDir + "/manifest.json"

	store := checkpoint.NewStore(cpPath)
	prior, err := store.Load()
	if err != nil {
		return nil, err
	}
	if err := checkpoint.Verify(prior, inputHash, indexHash); err != nil {
		return nil, err // exit code 3: version mismatch
	}
	resumedFrom := uint64(0)
	alreadyDone := map[uint64]bool{}
	if prior != nil {
		resumedFrom = prior.LastDoneAU
		for _, au := range prior.CompletedAU {
			alreadyDone[au] = true
		}
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	// Stage 1: bounded scanner. Sends block once the queue is full, which
	// stops upstream reading under downstream backpressure.
	auCh := make(chan *stream.AccessUnit, opts.QueueSize)
	scanErr := make(chan error, 1)
	go func() {
		defer close(auCh)
		sc, err := stream.NewScanner(newBytesReader(opts.Stream), opts.Codec, opts.StreamCfg)
		if err != nil {
			scanErr <- err
			return
		}
		for {
			au, err := sc.Next()
			if err != nil {
				scanErr <- err
				return
			}
			if au == nil {
				scanErr <- nil
				return
			}
			select {
			case auCh <- au:
			case <-ctx.Done():
				scanErr <- nil
				return
			}
		}
	}()

	// Stages 2..4: cut, silence, fingerprint on the shared timeline.
	tb := clock.NewTimeBase(40_000_000)
	cutter := segment.NewCutter(tb)
	hasher := fingerprint.NewHasher()
	detector := silence.DefaultDetector()
	cp := &checkpoint.State{
		FixtureID: opts.FixtureID, InputHash: inputHash, IndexHash: indexHash,
	}
	if prior != nil {
		cp.LastDoneAU, cp.CompletedAU = prior.LastDoneAU, append([]uint64(nil), prior.CompletedAU...)
	}

	var (
		aus       []*stream.AccessUnit
		outcomes  []SegmentOutcome
		committed int
		status    = StatusCompleted
		fatalErr  error
	)

consume:
	for {
		select {
		case <-ctx.Done():
			// Reached only on genuine external cancellation; our own
			// interrupt/write-failure paths set state before cancelling.
			if status != StatusInterrupted && fatalErr == nil {
				fatalErr = errs.New(errs.Fatal, errs.CodeCancelled, "pipeline cancelled")
			}
			break consume
		case au, ok := <-auCh:
			if !ok {
				break consume
			}
			aus = append(aus, au)
			segs := cutter.Cut([]*stream.AccessUnit{au}, 1)
			seg0 := segs[0]
			regions := detector.Analyze(seg0.Interval, silence.AudioFormat{HasAudio: false}, nil)
			digest := hasher.Hash(seg0.Payload)
			rtOK, err := container.VerifyRoundTrip(seg0.Payload)
			if err != nil {
				cancel()
				fatalErr = err
				break consume
			}
			outcomes = append(outcomes, SegmentOutcome{
				ID: seg0.ID, Hash: digest, Corrupt: seg0.Corrupt,
				RoundTripOK: rtOK, Silence: regions[0].Verdict.String(),
			})

			if !alreadyDone[au.Index] {
				cp.MarkDone(au.Index)
				if err := store.Save(cp); err != nil {
					cancel() // write failure: stop every stage
					fatalErr = err
					break consume
				}
				committed++
				if opts.InterruptAfter > 0 && prior == nil && committed == opts.InterruptAfter {
					status = StatusInterrupted
					cancel()
					break consume
				}
			}
		}
	}
	// Single join point. The scanner sends exactly one buffered result and
	// closes auCh on exit; draining the channel guarantees its goroutine is
	// not stranded even when the queue was full at cancellation time.
	if err := <-scanErr; err != nil && fatalErr == nil {
		fatalErr = err
	}
	for range auCh {
	}
	if fatalErr != nil {
		return nil, fatalErr
	}

	res := &Result{
		Status: status, ResumedFrom: resumedFrom, Segments: outcomes,
		InputHash: inputHash, IndexHash: indexHash,
		CheckpointPath: cpPath, ManifestPath: manifestPath,
	}
	for _, au := range aus {
		res.Damages = append(res.Damages, au.Damages...)
	}
	fillMatch(res, aus, opts, tb)

	if status == StatusInterrupted {
		return res, nil // checkpoint only; no partial manifest is ever published
	}

	if err := publishManifest(res, opts, outcomes, cp, manifestPath, store); err != nil {
		return nil, err
	}
	return res, nil
}
