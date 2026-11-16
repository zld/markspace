// Package demo runs the reproducible dual-path demonstration: the same
// seeded event schedule is fed through the trace path directly and through
// a real loopback UDP socket, both sharing one controller implementation,
// and their water-level curves and continuity summaries are reported.
package demo

import (
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"jitter/internal/buffer"
	"jitter/internal/clock"
	"jitter/internal/ingress"
	"jitter/internal/metrics"
	"jitter/internal/plc"
	"jitter/internal/wire"
)

// frameInterval is the 20 ms playback cadence.
const frameInterval = time.Duration(wire.FrameMs) * time.Millisecond

// Params configures one demo run.
type Params struct {
	Path             string // "udp", "trace" or "both"
	Seed             int64
	Frames           int
	Loss             float64
	Reorder          float64
	Duplicate        float64
	WaterlineEveryMs int
	CSVOut           string // "" writes the CSV to stdout
}

// Validate rejects bad parameters; the caller maps this to exit code 2.
func (p Params) Validate() error {
	switch p.Path {
	case "udp", "trace", "both":
	default:
		return fmt.Errorf("invalid --path %q (want udp, trace or both)", p.Path)
	}
	if p.Frames <= 0 {
		return fmt.Errorf("--frames must be positive, got %d", p.Frames)
	}
	for name, v := range map[string]float64{"loss": p.Loss, "reorder": p.Reorder, "duplicate": p.Duplicate} {
		if v < 0 || v >= 1 {
			return fmt.Errorf("--%s must be in [0,1), got %v", name, v)
		}
	}
	if p.WaterlineEveryMs <= 0 {
		return fmt.Errorf("--waterline-every-ms must be positive, got %d", p.WaterlineEveryMs)
	}
	return nil
}

// Run executes the demo and writes the waterline CSV and JSON summaries.
func Run(p Params, stdout io.Writer) error {
	if err := p.Validate(); err != nil {
		return err
	}
	events := ingress.GenerateTrace(ingress.TraceParams{
		Seed: p.Seed, Frames: p.Frames,
		Loss: p.Loss, Reorder: p.Reorder, Duplicate: p.Duplicate,
	})
	if len(events) == 0 {
		return fmt.Errorf("trace produced no events (loss too high for %d frames)", p.Frames)
	}

	paths := []string{"udp", "trace"}
	if p.Path != "both" {
		paths = []string{p.Path}
	}
	rec := metrics.NewRecorder(p.WaterlineEveryMs)
	var sums []metrics.Summary
	for _, path := range paths {
		evs := events
		var malformed uint64
		if path == "udp" {
			var err error
			evs, malformed, err = ingress.ExchangeUDP(events)
			if err != nil {
				return fmt.Errorf("udp path: %w", err)
			}
		}
		sums = append(sums, simulate(path, evs, p.Frames, malformed, rec))
	}

	csvOut := stdout
	if p.CSVOut != "" {
		f, err := os.Create(p.CSVOut)
		if err != nil {
			return fmt.Errorf("open --csv-out: %w", err)
		}
		defer f.Close()
		csvOut = f
	}
	if err := rec.WriteCSV(csvOut); err != nil {
		return fmt.Errorf("write waterline CSV: %w", err)
	}
	if err := metrics.WriteSummaries(stdout, sums); err != nil {
		return fmt.Errorf("write summaries: %w", err)
	}
	return nil
}

// simulate plays one event list through controller+PLC on a virtual clock
// and returns the aggregated summary. Deterministic for a given event list.
func simulate(path string, events []ingress.Event, frames int, malformed uint64, rec *metrics.Recorder) metrics.Summary {
	sort.SliceStable(events, func(a, b int) bool { return events[a].At < events[b].At })

	clk := clock.NewVirtual()
	ctrl := buffer.New(clk)
	concealer := plc.New(wire.SamplesPerFrame)

	// Playback starts once the initial target water level can be met.
	start := events[0].At + time.Duration(buffer.MinTarget)*frameInterval
	maxTicks := frames + 2000 // safety bound; normally frames + a few

	idx, framesOut := 0, 0
	for tick := 0; (framesOut < frames || idx < len(events)) && tick < maxTicks; tick++ {
		now := start + time.Duration(tick)*frameInterval
		clk.Set(now)
		for idx < len(events) && events[idx].At <= now {
			ctrl.Push(events[idx].Pkt)
			idx++
		}
		if framesOut >= frames {
			continue // drain trailing arrivals into the counters
		}
		pkt, ok := ctrl.Poll()
		if !ok && !ctrl.Playing() {
			continue // still buffering; the playback clock has not started
		}
		var pcm []int16
		if ok {
			pcm = wire.BytesToPCM(pkt.Payload)
		}
		concealer.Output(pcm, ok)
		framesOut++
		rec.Record(path, now, ctrl.Queued(), ctrl.Target())
	}

	st := ctrl.Stats()
	return metrics.Summary{
		Path:              path,
		FramesOut:         framesOut,
		Received:          st.Received,
		Malformed:         malformed,
		DropsDuplicate:    st.Duplicates,
		DropsLate:         st.Late,
		DropsOutOfWindow:  st.OutOfWindow,
		DropsQueueFull:    st.DroppedFull,
		ConcealedFrames:   concealer.Concealed,
		ContinuityOKRatio: concealer.ContinuityRatio(),
		MaxBoundaryJump:   concealer.MaxJump,
	}
}
