package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"mediasync/internal/clock"
	"mediasync/internal/demux"
	"mediasync/internal/pipeline"
	"mediasync/internal/remux"
	"mediasync/internal/rtprecover"
	"mediasync/internal/testmedia"
)

var errEOF = io.EOF

func (c Config) validate() error {
	if c.Workers <= 0 || c.Workers > pipeline.MaxWorkers {
		return fmt.Errorf("app: workers %d out of bounds (1..%d)", c.Workers, pipeline.MaxWorkers)
	}
	if c.Queue <= 0 || c.Queue > pipeline.MaxQueue {
		return fmt.Errorf("app: queue %d out of bounds (1..%d)", c.Queue, pipeline.MaxQueue)
	}
	if c.Loss < 0 || c.Loss > 0.5 {
		return fmt.Errorf("app: loss %f outside [0,0.5]", c.Loss)
	}
	if c.Clusters <= 0 || c.PerCluster <= 0 {
		return fmt.Errorf("app: cluster dimensions must be positive")
	}
	known := map[string]bool{
		testmedia.KindVideo: true, testmedia.KindAudio: true, testmedia.KindSubtitle: true,
	}
	for _, k := range c.Tracks {
		if !known[k] {
			return fmt.Errorf("app: unknown track kind %q", k)
		}
	}
	return nil
}

// driftMap applies the same configured ppm to every mapped track.
func driftMap(sources []uint32, ppm float64) map[uint32]float64 {
	out := make(map[uint32]float64, len(sources))
	for _, n := range sources {
		out[n] = ppm
	}
	return out
}

// timedSample pairs a demuxed sample with its ideal and reduced timestamps.
type timedSample struct {
	Sample   *demux.Sample
	IdealNS  int64
	ActualNS int64
}

// reduceSamples runs every sample through the shared timeline and measures
// the absolute sync error against the ideal (drift-free) timestamp. A
// monotonicity or wrap failure aborts the run rather than being hidden.
func reduceSamples(tl *clock.Timeline, samples []*demux.Sample, scaleNS int64) ([]float64, []timedSample, error) {
	syncErrs := make([]float64, 0, len(samples))
	timed := make([]timedSample, 0, len(samples))
	for _, s := range samples {
		idealNS := s.TimeTicks * scaleNS
		ns, err := tl.Reduce(s.Track, s.Seq, s.TimeTicks)
		if err != nil {
			return nil, nil, fmt.Errorf("clock reduction failed (track=%d seq=%d): %w",
				s.Track, s.Seq, err)
		}
		errMS := absFloat(float64(ns-idealNS)) / 1_000_000.0
		syncErrs = append(syncErrs, errMS)
		timed = append(timed, timedSample{Sample: s, IdealNS: idealNS, ActualNS: ns})
	}
	return syncErrs, timed, nil
}

// makeTaskSource converts ordered samples into a pipeline Source. Each task
// stamps its enqueue wall-clock time into clocks so the process function can
// measure end-to-end per-task latency under real concurrency.
func makeTaskSource(timed []timedSample, clocks *sync.Map) pipeline.Source {
	i := 0
	return func(context.Context) (pipeline.Task, error) {
		if i >= len(timed) {
			return pipeline.Task{}, io.EOF
		}
		ts := timed[i]
		i++
		task := pipeline.Task{
			Track: ts.Sample.Track, Seq: ts.Sample.Seq,
			NS: ts.ActualNS, Index: int64(i - 1),
			Data: append([]byte(nil), ts.Sample.Payload...),
		}
		clocks.Store(task.Index, time.Now())
		return task, nil
	}
}

func toRemuxSamples(merged []pipeline.Result) []remux.Sample {
	out := make([]remux.Sample, 0, len(merged))
	for _, r := range merged {
		out = append(out, remux.Sample{
			Track: r.Task.Track, Seq: r.Task.Seq,
			NS: r.Task.NS, Payload: r.Task.Data,
		})
	}
	return out
}

// rerunDigest renders the package a second time without publishing it and
// returns its payload digest, which must match the first run byte-for-byte.
func rerunDigest(mapping *demux.Mapping, d *demux.Demuxer, samples []remux.Sample) ([32]byte, error) {
	w, err := remux.NewWriter(mapping, d.Tracks(), d.ScaleNS())
	if err != nil {
		return [32]byte{}, err
	}
	var buf bytes.Buffer
	out, err := w.WriteTo(&buf, samples)
	if err != nil {
		return [32]byte{}, err
	}
	return out.PayloadHash, nil
}

// writeVideoSidecar emits the MP4 package for the video track when one is
// mapped; subtitle-only or audio-only runs skip it without failing.
func writeVideoSidecar(mp4Path string, d *demux.Demuxer, samples []remux.Sample) error {
	var video *demux.Track
	for _, tr := range d.Tracks() {
		if tr.Type == demux.TypeVideo {
			video = tr
			break
		}
	}
	if video == nil {
		return nil
	}
	f, err := os.Create(mp4Path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := remux.WriteMP4Sidecar(f, video, samples); err != nil {
		return err
	}
	return nil
}

// runRTPRecovery feeds a deterministic impaired stream through a bounded
// recoverer and returns its accounting, including propagated loss events.
func runRTPRecovery(cfg Config) (*rtprecover.Recoverer, rtprecover.Stats, error) {
	stream := testmedia.BuildRTPStream(testmedia.RTPStreamSpec{
		Seed: cfg.Seed, SSRC: 7, Packets: 500, ClockRate: 48000, FrameStep: 960,
		LossRate: cfg.Loss, ReorderRate: 0.1, DuplicateRate: 0.05,
	})
	tl := clock.NewTimeline()
	if err := tl.Add(clock.TrackClock{Track: 7, TicksPerSecond: 48000}); err != nil {
		return nil, rtprecover.Stats{}, err
	}
	rcv, err := rtprecover.NewRecoverer(7, 32, tl)
	if err != nil {
		return nil, rtprecover.Stats{}, err
	}
	delivered := 0
	for _, pkt := range stream.Packets {
		got, perr := rcv.Push(pkt)
		delivered += len(got)
		if perr != nil {
			// KindPacketLoss is expected for aged-out gaps and is reflected
			// in stats; anything else is a genuine internal failure.
			var ce *clock.Error
			if !errors.As(perr, &ce) || ce.Kind != clock.KindPacketLoss {
				return nil, rtprecover.Stats{}, perr
			}
		}
	}
	if _, ferr := rcv.Flush(); ferr != nil {
		var ce *clock.Error
		if !errors.As(ferr, &ce) || ce.Kind != clock.KindPacketLoss {
			return nil, rtprecover.Stats{}, ferr
		}
	}
	stats := rcv.Stats()
	if stats.Unrecoverable != len(stream.Lost) {
		return nil, stats, fmt.Errorf("%w: rtp losses accounted %d != truth %d",
			ErrInternal, stats.Unrecoverable, len(stream.Lost))
	}
	if delivered > stats.Received || delivered <= 0 {
		return nil, stats, fmt.Errorf("%w: implausible delivery count %d", ErrInternal, delivered)
	}
	return rcv, stats, nil
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func countVerdicts(vs []DamageVerdict, recoverable bool) int {
	n := 0
	for _, v := range vs {
		if v.Recoverable == recoverable {
			n++
		}
	}
	return n
}

func truncOffset(s demux.Summary) int64 {
	if s.Truncated != nil {
		return s.Truncated.Offset
	}
	return 0
}
