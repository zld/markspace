// Package planner validates time-aligned work against memory and concurrency
// limits and tracks changing throughput without exceeding the budget.
package planner

import (
	"fmt"
	"math"
	"sort"

	"pwflow/internal/fault"
	"pwflow/internal/timebase"
)

const (
	MiB = int64(1 << 20)

	baseOverheadMiB       int64 = 12
	perWorkerMiB          int64 = 8
	maxQueuedFrames             = 4
	defaultFrameBytes           = 320 * 240 * 3
	defaultPCMBytesPerSec       = 48000 * 2
)

// WindowEstimate describes one candidate segment to be scheduled.
type WindowEstimate struct {
	Index      int               `json:"index"`
	Interval   timebase.Interval `json:"interval"`
	FaultIDs   []string          `json:"fault_ids,omitempty"`
	FrameBytes int64             `json:"frame_bytes,omitempty"`
	FPS        int               `json:"fps"`
	Streamed   bool              `json:"streamed,omitempty"`
}

// Request is the pre-run budget evaluation input.
type Request struct {
	BudgetMiB   int64
	Workers     int
	InputBytes  int64
	Tracks      int
	Windows     []WindowEstimate
	AutoWorkers bool
}

// Plan is the accepted resource plan.
type Plan struct {
	Workers              int              `json:"workers"`
	BudgetBytes          int64            `json:"budget_bytes"`
	EstimatedMemoryBytes int64            `json:"estimated_memory_bytes"`
	BaseBytes            int64            `json:"base_bytes"`
	WorkerBytes          int64            `json:"worker_bytes"`
	InputReserveBytes    int64            `json:"input_reserve_bytes"`
	WindowWorkingBytes   int64            `json:"window_working_bytes"`
	MaxChunkBytes        int64            `json:"max_chunk_bytes"`
	Windows              []WindowEstimate `json:"windows"`
	CandidateScores      []CandidateScore `json:"candidate_scores"`
}

// CandidateScore makes window comparison explicit in logs and manifests.
type CandidateScore struct {
	Index          int   `json:"index"`
	DurationNS     int64 `json:"duration_ns"`
	EstimatedBytes int64 `json:"estimated_bytes"`
	Score          int64 `json:"score"`
}

// Evaluate compares candidate windows and concurrency with the memory cap.
// When AutoWorkers is false, an excessive requested worker count is rejected
// rather than silently changed. When true, the largest fitting count <=
// Workers is selected.
func Evaluate(req Request) (*Plan, error) {
	if req.BudgetMiB <= 0 {
		return nil, fault.New(fault.BudgetExceeded, "budget-mib must be positive")
	}
	budget := req.BudgetMiB * MiB
	workers := req.Workers
	if workers <= 0 {
		workers = 1
	}
	if req.Tracks > 16 {
		return nil, fault.New(fault.TooManyTracks, "track count %d exceeds 16", req.Tracks)
	}

	scores, windowWorking := scoreWindows(req.Windows)
	base := baseOverheadMiB * MiB
	inputReserve := req.InputBytes
	if inputReserve < 0 {
		inputReserve = 0
	}
	workerUnit := perWorkerMiB * MiB
	windowCap := 8 * MiB
	if windowWorking > windowCap {
		windowWorking = windowCap // streaming decodes do not retain every frame
	}

	maxWorkers := int((budget - base - inputReserve - windowWorking) / workerUnit)
	if maxWorkers < 1 {
		need := base + inputReserve + windowWorking + workerUnit
		return nil, fault.New(fault.BudgetExceeded,
			"budget %d MiB cannot start; minimum estimate %d MiB", req.BudgetMiB, ceilMiB(need))
	}
	if workers > maxWorkers {
		if !req.AutoWorkers {
			return nil, fault.New(fault.BudgetExceeded,
				"workers=%d needs about %d MiB; budget allows at most %d workers",
				workers, ceilMiB(base+inputReserve+windowWorking+int64(workers)*workerUnit), maxWorkers)
		}
		workers = maxWorkers
	}

	estimated := base + inputReserve + windowWorking + int64(workers)*workerUnit
	if estimated > budget {
		return nil, fault.New(fault.BudgetExceeded, "estimated %d bytes exceeds budget %d", estimated, budget)
	}
	chunk := int64(defaultFrameBytes) * maxQueuedFrames
	if chunk > budget/8 {
		chunk = budget / 8
	}
	return &Plan{
		Workers: workers, BudgetBytes: budget, EstimatedMemoryBytes: estimated,
		BaseBytes: base, WorkerBytes: int64(workers) * workerUnit,
		InputReserveBytes: inputReserve, WindowWorkingBytes: windowWorking,
		MaxChunkBytes: chunk, Windows: append([]WindowEstimate(nil), req.Windows...),
		CandidateScores: scores,
	}, nil
}

func scoreWindows(windows []WindowEstimate) ([]CandidateScore, int64) {
	out := make([]CandidateScore, 0, len(windows))
	var maxWorking int64
	for _, w := range windows {
		frameBytes := w.FrameBytes
		if frameBytes <= 0 {
			frameBytes = defaultFrameBytes
		}
		fps := int64(w.FPS)
		if fps <= 0 {
			fps = 30
		}
		durSec := float64(w.Interval.Duration()) / float64(timebase.Seconds(1))
		video := int64(durSec * float64(fps) * float64(frameBytes))
		audio := int64(durSec * float64(defaultPCMBytesPerSec))
		total := video + audio
		// Working set retains a bounded frame queue plus complete mono PCM.
		working := int64(maxQueuedFrames)*frameBytes + audio
		if working > maxWorking {
			maxWorking = working
		}
		// Lower score is better: shorter windows and smaller estimates.
		score := w.Interval.Duration()/timebase.Milliseconds(1) + total/MiB
		out = append(out, CandidateScore{Index: w.Index, DurationNS: w.Interval.Duration(), EstimatedBytes: total, Score: score})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score < out[j].Score })
	return out, maxWorking
}

func ceilMiB(v int64) int64 { return int64(math.Ceil(float64(v) / float64(MiB))) }

// BandwidthTracker is an EWMA throughput estimator. Observations are safe to
// feed from changing RTP arrival patterns.
type BandwidthTracker struct {
	Alpha      float64
	BpsEWMA    float64
	lastAtNS   int64
	haveLast   bool
	TotalBytes int64
}

// ObserveArrival updates throughput from bytes arriving at atNS. Zero or
// reversed time deltas are ignored rather than producing infinite rate.
func (b *BandwidthTracker) ObserveArrival(atNS int64, bytes int) float64 {
	if b.Alpha <= 0 || b.Alpha > 1 {
		b.Alpha = 0.3
	}
	b.TotalBytes += int64(bytes)
	if b.haveLast && atNS > b.lastAtNS {
		dtSec := float64(atNS-b.lastAtNS) / float64(timebase.Seconds(1))
		inst := float64(bytes*8) / dtSec
		if b.BpsEWMA == 0 {
			b.BpsEWMA = inst
		} else {
			b.BpsEWMA = b.Alpha*inst + (1-b.Alpha)*b.BpsEWMA
		}
	}
	b.lastAtNS = atNS
	b.haveLast = true
	return b.BpsEWMA
}

// AdaptiveChunk returns a chunk size bounded by both throughput and the
// memory plan. Throughput may grow prefetch depth, but never past maxBytes.
func AdaptiveChunk(bps float64, horizonNS int64, maxBytes int64) int64 {
	if maxBytes <= 0 {
		return 0
	}
	if bps <= 0 || horizonNS <= 0 {
		return maxBytes / 2
	}
	want := int64(bps / 8 * float64(horizonNS) / float64(timebase.Seconds(1)))
	if want <= 0 || want > maxBytes {
		return maxBytes
	}
	return want
}

// String makes budget errors easier to read in terminal output.
func (p *Plan) String() string {
	return fmt.Sprintf("workers=%d estimated=%dMiB budget=%dMiB",
		p.Workers, ceilMiB(p.EstimatedMemoryBytes), p.BudgetBytes/MiB)
}
