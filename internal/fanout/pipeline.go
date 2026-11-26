package fanout

import (
	"fmt"
	"sort"
)

// RateProfile describes one representation/bitrate fan-out lane.
type RateProfile struct {
	Index       int
	Name        string
	BitrateKbps int
}

// Profiles returns n deterministic representation profiles.
func Profiles(n int) []RateProfile {
	if n < 1 {
		n = 1
	}
	base := []int{600, 1200, 2500, 5000, 8000}
	out := make([]RateProfile, n)
	for i := 0; i < n; i++ {
		br := base[0]
		if i < len(base) {
			br = base[i]
		} else {
			br = base[len(base)-1] + (i-len(base)+1)*2500
		}
		out[i] = RateProfile{Index: i, Name: fmt.Sprintf("r%d", i), BitrateKbps: br}
	}
	return out
}

// ArtifactID is the stable manifest id for one interval/rate product.
func ArtifactID(rateIdx int, intervalID string) string {
	return fmt.Sprintf("r%d/%s", rateIdx, intervalID)
}

// RelPath is the work-dir-relative fragment path (slash form).
func RelPath(rateIdx int, intervalID string) string {
	return fmt.Sprintf("segments/r%d/%s.frag", rateIdx, intervalID)
}

// GroupReport is the fan-in result for one interval across representations.
type GroupReport struct {
	IntervalID      string
	Representations int
	Aligned         bool
	ErrorPackets    int
	IsolatedRates   []int
}

// FanInReport is the aggregate boundary/keyframe validation result.
type FanInReport struct {
	Groups           []GroupReport
	Aligned          bool
	MaxErrorPackets  int
	IsolatedArtifact []string
}

// FanIn checks that every representation of an interval shares start/end
// boundaries and the keyframe boundary. A mismatching representation is
// isolated and the group is not published as aligned.
func FanIn(frags []*ParsedFragment) FanInReport {
	byInterval := map[string][]*ParsedFragment{}
	var order []string
	for _, f := range frags {
		if _, ok := byInterval[f.Header.ID]; !ok {
			order = append(order, f.Header.ID)
		}
		byInterval[f.Header.ID] = append(byInterval[f.Header.ID], f)
	}
	sort.Strings(order)

	rep := FanInReport{Aligned: true}
	for _, id := range order {
		fs := byInterval[id]
		g := GroupReport{IntervalID: id, Representations: len(fs), Aligned: true}
		ref := fs[0].Header
		for _, f := range fs[1:] {
			h := f.Header
			offErr := maxAbs(ref.SourceStart-h.SourceStart, ref.SourceEnd-h.SourceEnd) / tsPacketSize
			tickMismatch := ref.StartTick != h.StartTick || ref.EndTick != h.EndTick
			kfMismatch := !equalInt64(f.MP4.KeyframeOff, fs[0].MP4.KeyframeOff)
			if offErr > int64(g.ErrorPackets) {
				g.ErrorPackets = int(offErr)
			}
			if offErr > 1 || tickMismatch || kfMismatch {
				g.Aligned = false
				g.IsolatedRates = append(g.IsolatedRates, h.RateIndex)
				rep.IsolatedArtifact = append(rep.IsolatedArtifact, ArtifactID(h.RateIndex, h.ID))
			}
		}
		if !g.Aligned {
			rep.Aligned = false
		}
		if g.ErrorPackets > rep.MaxErrorPackets {
			rep.MaxErrorPackets = g.ErrorPackets
		}
		rep.Groups = append(rep.Groups, g)
	}
	return rep
}

func maxAbs(d1, d2 int64) int64 {
	a, b := abs64(d1), abs64(d2)
	if a > b {
		return a
	}
	return b
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func equalInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
