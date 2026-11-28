// Package pipeline coordinates the release flow: parse, tick
// normalization, container/track/segment validation, offscreen decode
// and measurement, quality gates, budget enforcement, staged manifest
// migration and atomic publish with rollback points.
package pipeline

import (
	"fmt"
	"io"
	"os"
	"time"

	"mediacheck/internal/clock"
	"mediacheck/internal/codec"
	"mediacheck/internal/container"
	"mediacheck/internal/fault"
	"mediacheck/internal/manifest"
	"mediacheck/internal/sample"
	"mediacheck/internal/segment"
	"mediacheck/internal/track"
)

// Options configures one pipeline run.
type Options struct {
	Workdir        string
	Codecs         []codec.Kind
	CorruptOffset  int64 // -1 disables the corruption demo
	InterruptAfter string
	Recover        bool
	BudgetBytes    int64
	MaxFileBytes   int64
	MaxTracks      int
	Timeout        time.Duration
	SegTicks       clock.Ticks
}

func (o *Options) defaults() {
	if o.BudgetBytes == 0 {
		o.BudgetBytes = 1 << 20
	}
	if o.MaxFileBytes == 0 {
		o.MaxFileBytes = 16 << 20
	}
	if o.MaxTracks == 0 {
		o.MaxTracks = 8
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	if o.SegTicks == 0 {
		o.SegTicks = 9000
	}
}

// Run executes the pipeline, printing reviewable results to out, and
// returns the process exit code (0 ok, 2 input error, 3 interrupted).
func Run(opts Options, out io.Writer) int {
	opts.defaults()
	if err := run(opts, out); err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return fault.ExitCode(err)
	}
	return 0
}

func run(opts Options, out io.Writer) error {
	start := time.Now()
	if len(opts.Codecs) == 0 || len(opts.Codecs) > opts.MaxTracks {
		return fault.New(fault.ErrLimitExceeded, int64(len(opts.Codecs)),
			"codec count %d outside [1,%d]", len(opts.Codecs), opts.MaxTracks)
	}
	if err := os.RemoveAll(opts.Workdir); err != nil {
		return err
	}
	if err := os.MkdirAll(opts.Workdir, 0o755); err != nil {
		return err
	}

	// 1. generate and parse source streams
	assets := make([]*sample.Asset, 0, len(opts.Codecs))
	for _, k := range opts.Codecs {
		a, err := sample.Generate(k)
		if err != nil {
			return err
		}
		if int64(len(a.Stream)) > opts.MaxFileBytes {
			return fault.New(fault.ErrLimitExceeded, opts.MaxFileBytes,
				"stream %s exceeds %d bytes", k, opts.MaxFileBytes)
		}
		assets = append(assets, a)
		fmt.Fprintf(out, "parse codec=%s units=%d bytes=%d ok\n", k, len(a.Units), len(a.Stream))
	}

	// 2. corruption demo: damage must be traceable to the input offset
	if opts.CorruptOffset >= 0 {
		for _, a := range assets {
			bad := sample.CorruptAt(a.Stream, opts.CorruptOffset)
			_, err := codec.Units(bad, a.Kind)
			fe, ok := err.(*fault.Error)
			if !ok {
				return fault.New(fault.ErrIntegrity, opts.CorruptOffset,
					"corrupted %s stream parsed without error", a.Kind)
			}
			fmt.Fprintf(out, "corrupt codec=%s offset=%d code=%s\n", a.Kind, fe.Offset, fe.Code)
		}
	}

	// 3. boundary samples are generated and checked every run
	if err := boundarySamples(out); err != nil {
		return err
	}

	// 4. tick normalization on the shared integer line
	var align clock.Aligner
	mappings := make([]track.Mapping, 0, len(assets))
	for i, a := range assets {
		if err := track.CheckMonotonic(a.Ticks); err != nil {
			return err
		}
		for _, t := range a.Ticks {
			_, frac := clock.Convert(t, clock.TBMedia, clock.TBMilli)
			align.Observe(frac)
		}
		mappings = append(mappings, track.Mapping{TrackID: uint32(i + 1), Kind: a.Kind, SrcIndex: i})
		fmt.Fprintf(out, "ticks codec=%s units=%d monotonic=true\n", a.Kind, len(a.Ticks))
	}
	fmt.Fprintf(out, "alignment max_error=%.4f ticks\n", align.Max())

	// 5. container build + read-back check, 6. track mapping rules
	specs := make([]container.TrackSpec, 0, len(assets))
	for i, a := range assets {
		specs = append(specs, trackSpec(uint32(i+1), a))
	}
	mp4data, err := container.Build(specs)
	if err != nil {
		return err
	}
	rep, err := container.Check(mp4data, specs)
	if err != nil {
		return err
	}
	for _, tr := range rep.Tracks {
		fmt.Fprintf(out, "container track=%d codec=%s timescale=%d samples=%d keys=%d ok\n",
			tr.ID, tr.Kind, tr.Timescale, tr.SampleCount, tr.KeyCount)
	}
	required := make([]codec.Kind, len(opts.Codecs))
	copy(required, opts.Codecs)
	if err := track.ValidateMappings(mappings, required, opts.MaxTracks); err != nil {
		return err
	}
	fmt.Fprintf(out, "tracks mappings=%d missing=0 duplicates=0 ok\n", len(mappings))

	// 7. keyframe index, 8. segments + RTP boundaries
	var segEntries []manifest.SegmentEntry
	for _, a := range assets {
		if err := a.Index.Validate(a.Units, int64(len(a.Stream))); err != nil {
			return err
		}
		segs, err := segment.Cut(a.Units, a.Ticks, opts.SegTicks, a.Stream)
		if err != nil {
			return err
		}
		for _, s := range segs {
			lo := s.Units[0].StartCode
			hi := s.Units[len(s.Units)-1].Offset + int64(s.Units[len(s.Units)-1].Size)
			n, err := segment.CheckRTP(a.Stream[lo:hi], 96, 0x5000+uint32(a.Kind), uint32(s.StartTick), 0, 600)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "segment codec=%s index=%d ticks=%d-%d bytes=%d sha256=%s packets=%d\n",
				a.Kind, s.Index, s.StartTick, s.EndTick, s.Bytes, s.SHA256, n)
			segEntries = append(segEntries, manifest.SegmentEntry{
				Codec: a.Kind.String(), Index: s.Index, StartTick: int64(s.StartTick),
				EndTick: int64(s.EndTick), SHA256: s.SHA256, Packets: n,
			})
		}
		fmt.Fprintf(out, "index codec=%s keyframes=%d ok\n", a.Kind, len(a.Index.Offsets))
	}

	// 9. offscreen decode + measurement
	var frameEntries []manifest.FrameEntry
	totalBytes := int64(len(mp4data))
	for _, a := range assets {
		totalBytes += int64(len(a.Stream))
		frames := sample.DecodeFrames(a.Kind, len(a.Index.Offsets)*3)
		for i, f := range frames {
			st := measureFrame(f)
			totalBytes += int64(len(f))
			fmt.Fprintf(out, "frame codec=%s index=%d sha256=%s min=%d max=%d mean=%.2f\n",
				a.Kind, i, st.sha, st.min, st.max, st.mean)
			frameEntries = append(frameEntries, manifest.FrameEntry{
				Codec: a.Kind.String(), Index: i, SHA256: st.sha,
				Min: st.min, Max: st.max, Mean: st.mean,
			})
		}
	}
	peak := loudnessPeakDBFS(sample.Tone(4800))
	fmt.Fprintf(out, "loudness peak_dbfs=%.2f\n", peak)

	// 10. quality gates first, then the processing budget
	switch {
	case peak > 0:
		return fault.New(fault.ErrQualityGate, fault.NoOffset, "loudness peak %.2f dBFS above 0", peak)
	case align.Max() > 1.0:
		return fault.New(fault.ErrQualityGate, fault.NoOffset, "alignment error %.4f exceeds 1 tick", align.Max())
	}
	if totalBytes > opts.BudgetBytes {
		return fault.New(fault.ErrBudgetExceeded, fault.NoOffset,
			"processed %d bytes exceed budget %d; stopping without publishing", totalBytes, opts.BudgetBytes)
	}
	if time.Since(start) > opts.Timeout {
		return fault.New(fault.ErrLimitExceeded, fault.NoOffset, "processing deadline %s exceeded", opts.Timeout)
	}

	// 11. publish v0, migrate to v1 in stages, publish v1
	return publish(opts, out, segEntries, frameEntries, peak, align.Max())
}
