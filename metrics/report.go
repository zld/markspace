package metrics

import (
	"encoding/json"
	"io"
	"math"
	"sort"
	"strconv"
	"time"
)

// Report is one JSONL acceptance/verification line.
type Report struct {
	Case              string  `json:"case"`
	OK                bool    `json:"ok"`
	SyncMS            float64 `json:"sync_ms"`
	Loss              int     `json:"loss"`
	Recovered         int     `json:"recovered"`
	RecoverRate       string  `json:"recover_rate"`
	RecoverRatePct    float64 `json:"recover_rate_pct"`
	Duplicate         int     `json:"duplicate"`
	BadSegments       int     `json:"bad_segments"`
	Fallback          int     `json:"fallback"`
	Unresolved        int     `json:"unresolved"`
	Resumed           int     `json:"resumed"`
	ArchiveSHA256     string  `json:"archive_sha256,omitempty"`
	ContinuousSHA256  string  `json:"continuous_sha256,omitempty"`
	ArchiveMatch      bool    `json:"archive_match,omitempty"`
	P95MS             float64 `json:"p95_ms"`
	InFlightPeakBytes int64   `json:"inflight_peak_bytes"`
	Events            int     `json:"events,omitempty"`
	CheckpointCursor  int64   `json:"checkpoint_cursor,omitempty"`
	Error             string  `json:"error,omitempty"`
}

// WriteJSONL writes exactly one JSON object followed by newline.
func (r Report) WriteJSONL(w io.Writer) error {
	enc := json.NewEncoder(w)
	return enc.Encode(r)
}

// P95Milliseconds returns nearest-rank P95 from durations.
func P95Milliseconds(ds []time.Duration) float64 {
	if len(ds) == 0 {
		return 0
	}
	xs := append([]time.Duration(nil), ds...)
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	idx := int(math.Ceil(0.95*float64(len(xs)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(xs) {
		idx = len(xs) - 1
	}
	return float64(xs[idx].Microseconds()) / 1000.0
}

// RecoverRate builds both numeric and presentation recovery-rate fields.
func RecoverRate(recovered, loss int) (string, float64) {
	if loss <= 0 {
		return "100%", 100
	}
	pct := 100 * float64(recovered) / float64(loss)
	return formatPercent(pct), math.Round(pct*10) / 10
}

func formatPercent(v float64) string {
	if math.Abs(v-math.Round(v)) < 0.05 {
		return strconv.FormatInt(int64(math.Round(v)), 10) + "%"
	}
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) + "%"
}

// MaxAVSyncNS matches audio and video PTS values in order and returns the
// largest absolute difference. Inputs need not be sorted.
func MaxAVSyncNS(audioPTS, videoPTS []int64) int64 {
	if len(audioPTS) == 0 || len(videoPTS) == 0 {
		return 0
	}
	a := append([]int64(nil), audioPTS...)
	v := append([]int64(nil), videoPTS...)
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })

	maxDiff := int64(0)
	for _, vp := range v {
		// closest audio PTS
		i := sort.Search(len(a), func(i int) bool { return a[i] >= vp })
		best := int64(-1)
		for _, j := range []int{i - 1, i} {
			if j >= 0 && j < len(a) {
				d := abs64(a[j] - vp)
				if best < 0 || d < best {
					best = d
				}
			}
		}
		if best > maxDiff {
			maxDiff = best
		}
	}
	return maxDiff
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
