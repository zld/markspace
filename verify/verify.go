// Package verify independently re-checks pipeline output against the
// delivery constraints: timestamp monotonicity, cross-representation
// boundary alignment within one unified clock tick, track parameters and
// manifest references.
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"continuity/manifest"
	"continuity/media"
	"continuity/planner"
)

// Report is the verification outcome; it is also the CLI's metrics JSON.
type Report struct {
	RoundtripRate          float64  `json:"roundtrip_rate"`
	KeyframeHitRate        float64  `json:"keyframe_hit_rate"`
	AlignedBoundaryDeltaNs int64    `json:"aligned_boundary_delta_ns"`
	Verified               bool     `json:"verified"`
	Failures               []string `json:"failures,omitempty"`
}

// Check re-parses the output directory and validates every constraint.
// A failed check yields a non-nil error of kind KindVerify and the
// report is still returned for inspection.
func Check(dir string, m *manifest.Manifest, inputs []*media.Container,
	plan *planner.Plan, hitRate float64) (*Report, error) {

	rep := &Report{KeyframeHitRate: hitRate}
	fail := func(format string, args ...interface{}) {
		rep.Failures = append(rep.Failures, fmt.Sprintf(format, args...))
	}

	// Manifest must reference the actual inputs and plan.
	if m.PlanHash != plan.PlanHash {
		fail("manifest plan hash %s != current plan %s", m.PlanHash, plan.PlanHash)
	}
	if len(m.InputHashes) != len(inputs) {
		fail("manifest lists %d inputs, have %d", len(m.InputHashes), len(inputs))
	} else {
		for i, in := range inputs {
			if m.InputHashes[i] != in.FileSHA256 {
				fail("input %d hash mismatch", i)
			}
		}
	}

	// Every referenced segment artifact must exist and hash-match.
	matched := 0
	for _, s := range m.Segments {
		raw, err := os.ReadFile(filepath.Join(dir, s.File))
		if err != nil {
			fail("segment r%d/s%d unreadable: %v", s.Rep, s.Index, err)
			continue
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != s.SHA256 {
			fail("segment r%d/s%d hash mismatch", s.Rep, s.Index)
			continue
		}
		matched++
	}
	if len(m.Segments) > 0 {
		rep.RoundtripRate = float64(matched) / float64(len(m.Segments))
	}

	// Per-representation monotonicity and contiguity.
	byRep := map[int][]manifest.SegmentRef{}
	for _, s := range m.Segments {
		byRep[s.Rep] = append(byRep[s.Rep], s)
	}
	for r, segs := range byRep {
		sort.Slice(segs, func(i, j int) bool { return segs[i].Index < segs[j].Index })
		for i, s := range segs {
			if i == 0 && s.StartNanos != 0 {
				fail("rep %d first segment starts at %d, not 0", r, s.StartNanos)
			}
			if i > 0 {
				prev := segs[i-1]
				if s.StartNanos <= prev.StartNanos {
					fail("rep %d segment %d start not monotonic", r, s.Index)
				}
				if s.StartNanos != prev.EndNanos {
					fail("rep %d segment %d not contiguous with previous", r, s.Index)
				}
			}
			if s.EndNanos <= s.StartNanos {
				fail("rep %d segment %d has non-positive duration", r, s.Index)
			}
		}
	}

	// Cross-representation boundary alignment within one unified tick.
	var timescales []uint32
	for _, in := range inputs {
		for _, t := range in.Tracks {
			timescales = append(timescales, t.Timescale)
		}
	}
	byIndex := map[int][]int64{}
	for _, s := range m.Segments {
		byIndex[s.Index] = append(byIndex[s.Index], s.StartNanos)
	}
	for idx, starts := range byIndex {
		lo, hi := starts[0], starts[0]
		for _, v := range starts {
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
		if d := hi - lo; d > rep.AlignedBoundaryDeltaNs {
			rep.AlignedBoundaryDeltaNs = d
		}
		ok, err := media.WithinTick(hi-lo, timescales...)
		if err != nil {
			fail("tick computation: %v", err)
		} else if !ok {
			fail("segment %d boundary delta %d ns exceeds one unified tick", idx, hi-lo)
		}
	}

	// Track parameters in the manifest must match the plan and inputs.
	for i, r := range m.Representations {
		if i >= len(inputs) {
			fail("manifest representation %d has no input", i)
			break
		}
		v := inputs[i].VideoTrack()
		if r.Width != v.Width || r.Height != v.Height {
			fail("rep %d dimensions %dx%d != probed %dx%d",
				i, r.Width, r.Height, v.Width, v.Height)
		}
		if r.ColorOutput != plan.ColorOutput {
			fail("rep %d color output does not match the plan", i)
		}
	}

	rep.Verified = len(rep.Failures) == 0 && rep.RoundtripRate == 1.0
	if !rep.Verified {
		return rep, &media.Error{Kind: media.KindVerify, Op: "verify.check",
			Recoverable: true,
			Msg:         strings.Join(rep.Failures, "; ")}
	}
	return rep, nil
}
