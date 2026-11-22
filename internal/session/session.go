// Package session wires generated captures through transport recovery,
// ingest validation, timeline composition and offscreen export. Every stage
// runs behind a fixed-capacity queue so memory does not grow with input.
package session

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"

	"avcheck/internal/ingest"
	"avcheck/internal/media"
	"avcheck/internal/pipeline"
	"avcheck/internal/render"
	"avcheck/internal/timeline"
)

// Config describes one demo or regression run.
type Config struct {
	Seed          int64
	Frames        int
	Tracks        int
	Queue         int
	Width         int // export raster width
	Height        int // export raster height
	KeyInterval   int
	Timescale     int64
	StartFrameSeq uint64
	StartTicks    int64
	TrackTickSkew int64
	TicksPerFrame int64
	BurstThresh   int
	ReorderWindow int
	SegFrames     int
	OutDir        string
	Policy        ingest.Policy
}

// Defaults fills zero values with the acceptance defaults.
func (c *Config) Defaults() {
	if c.Seed == 0 {
		c.Seed = 41
	}
	if c.Frames <= 0 {
		c.Frames = 1200
	}
	if c.Tracks <= 0 {
		c.Tracks = 3
	}
	if c.Queue <= 0 {
		c.Queue = 32
	}
	if c.Width <= 0 {
		c.Width = 1920
	}
	if c.Height <= 0 {
		c.Height = 1080
	}
	if c.KeyInterval <= 0 {
		c.KeyInterval = 25
	}
	if c.Timescale <= 0 {
		c.Timescale = 90000
	}
	if c.TicksPerFrame <= 0 {
		c.TicksPerFrame = 3000
	}
	if c.TrackTickSkew == 0 {
		c.TrackTickSkew = 137
	}
	if c.BurstThresh <= 0 {
		c.BurstThresh = 3
	}
	if c.ReorderWindow <= 0 {
		c.ReorderWindow = 16
	}
	if c.SegFrames <= 0 {
		c.SegFrames = c.Frames / 12
		if c.SegFrames < 1 {
			c.SegFrames = 1
		}
	}
}

// GenConfig returns the capture configuration matching this session.
func (c Config) GenConfig() ingest.GenConfig {
	return ingest.GenConfig{
		Seed: c.Seed, Frames: c.Frames, Tracks: c.Tracks,
		Width: c.Width, Height: c.Height, StartFrameSeq: c.StartFrameSeq,
		StartTicks: c.StartTicks, TrackTickSkew: c.TrackTickSkew,
		TicksPerFrame: c.TicksPerFrame, KeyInterval: c.KeyInterval,
	}
}

// TrackResult holds one track's recovery tally and its MP4 index.
type TrackResult struct {
	ID          uint32
	Received    uint64
	Duplicates  uint64
	Reordered   uint64
	Late        uint64
	GapEvents   uint64
	BurstGaps   uint64
	GapPackets  uint64
	Corrupt     uint64
	Truncated   uint64
	Rejected    uint64
	Accepted    uint64
	MP4File     string
	Samples     int
	IndexHits   uint64
	IndexMisses uint64
	State       timeline.TrackState
	Failure     error
}

// Result is the whole run's outcome.
type Result struct {
	Config    Config
	Manifest  *render.Manifest
	Tracks    []TrackResult
	Segments  int
	Faults    FaultCounts
	QueuePeak int64
	QueueCap  int
	Elapsed   string
	Roundtrip int
	Hits      uint64
	Lookups   uint64
	Burned    int // tracks failed under fail-track policy
}

// KeyframeHitRate aggregates per-track index lookups.
func (r *Result) KeyframeHitRate() float64 {
	if r.Lookups == 0 {
		return 1
	}
	return float64(r.Hits) / float64(r.Lookups)
}

// RoundtripConsistency is verified frames / exported frames.
func (r *Result) RoundtripConsistency() float64 {
	if r.Manifest == nil || r.Manifest.Frames == 0 {
		return 1
	}
	return float64(r.Roundtrip) / float64(r.Manifest.Frames)
}

type wirePacket struct {
	track int
	raw   []byte
}

type recoveredFrame struct {
	track int
	frame *media.Frame
}

// Run executes the pipeline and writes the export directory.
func Run(ctx context.Context, cfg Config, faults Faults) (*Result, error) {
	cfg.Defaults()
	clock, err := media.NewClock(cfg.Timescale)
	if err != nil {
		return nil, err
	}
	gen := cfg.GenConfig()
	lo := cfg.StartFrameSeq
	hi := cfg.StartFrameSeq + uint64(cfg.Frames) - 1
	ids := make([]uint32, cfg.Tracks)
	for i := range ids {
		ids[i] = uint32(i + 1)
	}
	sc, err := timeline.NewStreamComposer(clock, ids, lo, hi, 8*cfg.Queue)
	if err != nil {
		return nil, err
	}
	for _, sub := range []string{"mp4", "png"} {
		if err := os.MkdirAll(filepath.Join(cfg.OutDir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	writer, err := render.NewWriter(filepath.Join(cfg.OutDir, "png"), cfg.Tracks, render.Options{
		SegFrames: cfg.SegFrames, Clock: clock,
	}, rasterizer(gen))
	if err == nil {
		// The manifest also lives at the export root for consumers that do
		// not know the png/ layout.
		writer.Mirror(filepath.Join(cfg.OutDir, "manifest.json"))
	}
	if err != nil {
		return nil, err
	}

	packets := pipeline.NewQueue[wirePacket](cfg.Queue, "capture")
	frames := pipeline.NewQueue[recoveredFrame](cfg.Queue, "frames")
	stats := make([]TrackResult, cfg.Tracks)
	builders := make([]*ingest.MP4Builder, cfg.Tracks)
	for i := range builders {
		b, err := ingest.NewMP4Builder(mp4Path(cfg.OutDir, i))
		if err != nil {
			return nil, err
		}
		builders[i] = b
		stats[i].ID = uint32(i + 1)
	}

	produce := func(ctx context.Context) error {
		defer packets.Close()
		return producePackets(ctx, packets, gen, cfg, faults, stats)
	}
	decode := func(ctx context.Context) error {
		defer frames.Close()
		return decodeValidate(ctx, packets, frames, cfg, stats, builders)
	}
	compose := func(ctx context.Context) error {
		return composeExport(ctx, frames, sc, writer)
	}
	res := &Result{Config: cfg, QueueCap: cfg.Queue}
	if runErr := pipeline.Run(ctx, produce, decode, compose); runErr != nil {
		if me, ok := runErr.(*media.Error); !ok || me.Kind != media.KindCanceled {
			abortBuilders(builders)
			return nil, runErr
		}
	}
	res.QueuePeak = maxInt64(packets.Peak(), frames.Peak())
	for i, b := range builders {
		if stats[i].State == timeline.TrackFailed {
			b.Abort()
			continue
		}
		if err := b.Close(uint32(cfg.Timescale)); err != nil {
			return nil, err
		}
		st, err := ingest.ReadSampleTable(mp4Path(cfg.OutDir, i))
		if err != nil {
			return nil, err
		}
		times, err := st.Times(clock)
		if err != nil {
			return nil, err
		}
		ki, err := st.KeyIndex(clock)
		if err != nil {
			return nil, err
		}
		for _, t := range times {
			if _, ok := ki.Locate(t); ok {
				stats[i].IndexHits++
			} else {
				stats[i].IndexMisses++
			}
		}
		stats[i].MP4File = filepath.ToSlash(filepath.Join("mp4", filepath.Base(mp4Path(cfg.OutDir, i))))
		stats[i].Samples = len(st.Samples)
	}
	man, err := writer.Finish()
	if err != nil {
		return nil, err
	}
	res.Manifest = man
	res.Segments = len(man.Segments)
	res.Roundtrip = writer.RoundtripOK
	for _, tr := range stats {
		res.Tracks = append(res.Tracks, tr)
		res.Hits += tr.IndexHits
		res.Lookups += tr.IndexHits + tr.IndexMisses
		res.Faults.Duplicated += int(tr.Duplicates)
		res.Faults.Reordered += int(tr.Reordered)
		res.Faults.Corrupted += int(tr.Corrupt)
		res.Faults.Truncated += int(tr.Truncated)
		res.Faults.Lost += int(tr.GapPackets)
		res.Faults.Bursts += int(tr.BurstGaps)
		if tr.Failure != nil {
			res.Burned++
		}
	}
	man.Inputs = render.InputStats{
		Received:   countSum(stats, func(t TrackResult) uint64 { return t.Received }),
		Duplicates: countSum(stats, func(t TrackResult) uint64 { return t.Duplicates }),
		Reordered:  countSum(stats, func(t TrackResult) uint64 { return t.Reordered }),
		Late:       countSum(stats, func(t TrackResult) uint64 { return t.Late }),
		Gaps:       countSum(stats, func(t TrackResult) uint64 { return t.GapEvents }),
		BurstGaps:  countSum(stats, func(t TrackResult) uint64 { return t.BurstGaps }),
		Corrupt:    countSum(stats, func(t TrackResult) uint64 { return t.Corrupt }),
		Truncated:  countSum(stats, func(t TrackResult) uint64 { return t.Truncated }),
		Rejected:   countSum(stats, func(t TrackResult) uint64 { return t.Rejected }),
	}
	if err := writer.Rewrite(); err != nil {
		return nil, err
	}
	// Rewrite recomputes the digest over the manifest including the input
	// tally, so refresh the in-memory copy the caller compares against.
	res.Manifest = writer.Manifest()
	return res, nil
}

func mp4Path(dir string, i int) string {
	return filepath.Join(dir, "mp4", fmt.Sprintf("track_%02d.mp4", i+1))
}

func abortBuilders(b []*ingest.MP4Builder) {
	for _, x := range b {
		if x != nil {
			x.Abort()
		}
	}
}

func countSum(stats []TrackResult, f func(TrackResult) uint64) uint64 {
	var n uint64
	for _, s := range stats {
		n += f(s)
	}
	return n
}

func maxInt64(vs ...int64) int64 {
	var m int64
	for _, v := range vs {
		if v > m {
			m = v
		}
	}
	return m
}

// rasterizer renders one output slot as an RGBA image using only the slot
// itself, so a separate verify process can reproduce it byte for byte: real
// frames are regenerated from their identity, placeholders use the gap
// pattern, and any frame that was not clean is tinted.
func rasterizer(gen ingest.GenConfig) render.Renderer {
	return func(of timeline.OutputFrame) *image.RGBA {
		if of.Primary == nil {
			return ingest.Placeholder(gen.Width, gen.Height)
		}
		track := int(of.Primary.StreamID) - 1
		img := ingest.Image(gen, track, of.Primary.Seq)
		if of.Primary.State != media.StateClean {
			// Tint recovered or degraded content so a regression is visible
			// without reading the manifest.
			for i := 0; i < len(img.Pix); i += 4 {
				img.Pix[i] = uint8(int(img.Pix[i])/2 + 30)
			}
		}
		return img
	}
}
