// Package metrics records water-level curves and aggregates continuity
// summaries for each receive path.
package metrics

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"
)

// WaterlinePoint is one sample of the queue/target water-level curve.
type WaterlinePoint struct {
	Path      string
	ElapsedMs int64
	Queued    int
	Target    int
}

// Recorder samples water levels per path at a fixed interval.
type Recorder struct {
	everyMs int64

	mu     sync.Mutex
	points []WaterlinePoint
	last   map[string]int64
}

func NewRecorder(everyMs int) *Recorder {
	if everyMs <= 0 {
		everyMs = 200
	}
	return &Recorder{everyMs: int64(everyMs), last: make(map[string]int64)}
}

// Record appends a point if at least the configured interval has elapsed
// since the previous point for this path.
func (r *Recorder) Record(path string, now time.Duration, queued, target int) {
	ms := now.Milliseconds()
	r.mu.Lock()
	defer r.mu.Unlock()
	if last, ok := r.last[path]; ok && ms-last < r.everyMs {
		return
	}
	r.last[path] = ms
	r.points = append(r.points, WaterlinePoint{Path: path, ElapsedMs: ms, Queued: queued, Target: target})
}

// WriteCSV writes the water-level curve with header
// path,elapsed_ms,queued_frames,target_frames.
func (r *Recorder) WriteCSV(w io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"path", "elapsed_ms", "queued_frames", "target_frames"}); err != nil {
		return err
	}
	for _, p := range r.points {
		row := []string{
			p.Path,
			strconv.FormatInt(p.ElapsedMs, 10),
			strconv.Itoa(p.Queued),
			strconv.Itoa(p.Target),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// Summary aggregates one path's reception and continuity results.
type Summary struct {
	Path              string  `json:"path"`
	FramesOut         int     `json:"frames_out"`
	Received          uint64  `json:"received"`
	Malformed         uint64  `json:"malformed"`
	DropsDuplicate    uint64  `json:"drops_duplicate"`
	DropsLate         uint64  `json:"drops_late"`
	DropsOutOfWindow  uint64  `json:"drops_out_of_window"`
	DropsQueueFull    uint64  `json:"drops_queue_full"`
	Realigned         uint64  `json:"realigned"`
	ConcealedFrames   int     `json:"concealed_frames"`
	ContinuityOKRatio float64 `json:"continuity_ok_ratio"`
	MaxBoundaryJump   int     `json:"max_boundary_jump"`
}

// WriteSummaries writes one JSON object per line so summaries are easy to
// diff between seeded runs.
func WriteSummaries(w io.Writer, sums []Summary) error {
	for _, s := range sums {
		b, err := json.Marshal(s)
		if err != nil {
			return fmt.Errorf("metrics: marshal summary: %w", err)
		}
		if _, err := fmt.Fprintf(w, "%s\n", b); err != nil {
			return err
		}
	}
	return nil
}
