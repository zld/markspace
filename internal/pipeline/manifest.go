package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	"mediad/internal/media"
)

// SegmentStatus marks how completely a segment could be delivered.
type SegmentStatus string

const (
	SegOK            SegmentStatus = "ok"
	SegGap           SegmentStatus = "gap"
	SegUnrecoverable SegmentStatus = "unrecoverable"
)

// Segment is one delivery window of one track with its integrity hash.
type Segment struct {
	Index   int           `json:"index"`
	Track   int           `json:"track"`
	StartNs int64         `json:"start_ns"`
	EndNs   int64         `json:"end_ns"`
	Samples int           `json:"samples"`
	SHA256  string        `json:"sha256"`
	Status  SegmentStatus `json:"status"`
}

// Manifest is the delivery manifest: per-segment hashes, recovered gaps
// and the measured clock drift.
type Manifest struct {
	Segments []Segment   `json:"segments"`
	Gaps     []media.Gap `json:"gaps"`
	DriftPPM float64     `json:"drift_ppm"`
	Status   string      `json:"status"` // ok | recovered | unrecoverable
}

// WriteJSON emits the manifest as indented JSON.
func (m *Manifest) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

// SegmentBounds splits [start, end) into n equal windows.
func SegmentBounds(start, end int64, n int) [][2]int64 {
	out := make([][2]int64, n)
	span := end - start
	for i := 0; i < n; i++ {
		out[i] = [2]int64{
			start + span*int64(i)/int64(n),
			start + span*int64(i+1)/int64(n),
		}
	}
	return out
}

// HashPayloads hashes the concatenation of the given payloads.
func HashPayloads(payloads [][]byte) string {
	h := sha256.New()
	for _, p := range payloads {
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// BuildSegments assigns events to time windows and hashes each window's
// compressed payloads. Windows overlapping a gap or containing damaged
// samples are flagged rather than silently delivered as complete.
func BuildSegments(track int, events []media.Event, outTimes []int64, bounds [][2]int64, gaps []media.Gap) []Segment {
	segs := make([]Segment, len(bounds))
	for i, b := range bounds {
		segs[i] = Segment{Index: i, Track: track, StartNs: b[0], EndNs: b[1], Status: SegOK}
	}
	gapAt := func(seq int) bool {
		for _, g := range gaps {
			if seq >= g.FromSeq && seq < g.ToSeq {
				return true
			}
		}
		return false
	}
	for i, ev := range events {
		if i >= len(outTimes) {
			break
		}
		t := outTimes[i]
		si := sort_window(bounds, t)
		if si < 0 {
			continue
		}
		s := &segs[si]
		if ev.Damaged || gapAt(ev.Seq) {
			if s.Status == SegOK {
				s.Status = SegGap
			}
			continue
		}
		s.Samples++
	}
	// Hash per window in a second pass to keep payloads grouped.
	payloads := make([][][]byte, len(bounds))
	for i, ev := range events {
		if i >= len(outTimes) || ev.Damaged || gapAt(ev.Seq) {
			continue
		}
		si := sort_window(bounds, outTimes[i])
		if si < 0 {
			continue
		}
		payloads[si] = append(payloads[si], ev.Payload)
	}
	for i := range segs {
		segs[i].SHA256 = HashPayloads(payloads[i])
	}
	return segs
}

// sort_window returns the window containing t, or -1.
func sort_window(bounds [][2]int64, t int64) int {
	lo, hi := 0, len(bounds)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case t < bounds[mid][0]:
			hi = mid - 1
		case t >= bounds[mid][1]:
			lo = mid + 1
		default:
			return mid
		}
	}
	return -1
}
