package mediacore

import (
	"sort"
)

// Known channel layouts and their channel counts. Anything outside this map is
// unrecognizable and causes the owning track to be rejected.
var KnownLayouts = map[string]int{
	"mono":   1,
	"stereo": 2,
	"5.1":    6,
	"7.1":    8,
}

// SourceMeta identifies where a metadata observation came from.
type SourceMeta struct {
	Asset   string
	Version string
	Track   uint32
}

// AudioSpec is a concrete audio configuration.
type AudioSpec struct {
	Rate     uint32
	Layout   string
	Channels int
}

// AudioCandidate is one version's declared audio configuration.
type AudioCandidate struct {
	SourceMeta
	Rate   uint32
	Layout string
}

// AudioConversion records a planned reconciliation of one source to target.
type AudioConversion struct {
	Source SourceMeta
	From   AudioSpec
	To     AudioSpec
}

// AudioDecision is the reconciled audio plan for one logical track.
type AudioDecision struct {
	Target      AudioSpec
	Explicit    bool // an explicit target configuration was supplied
	Conversions []AudioConversion
}

// ResolveAudio reconciles channel layout and sample rate. An unrecognized
// layout rejects its owning track with ErrUnknownLayout and a stated reason.
// When sources disagree, an explicit target drives conversions; without one
// the disagreement is an unresolvable conflict.
func ResolveAudio(asset string, cands []AudioCandidate, target AudioSpec, explicit bool) (*AudioDecision, []*Error) {
	var rejects []*Error
	specs := make([]AudioSpec, 0, len(cands))
	for _, c := range cands {
		ch, ok := KnownLayouts[c.Layout]
		if !ok {
			rejects = append(rejects, WrapMsg(ErrUnknownLayout, c.Asset, c.Track, StageMetadata, -1,
				"unrecognized channel layout \""+c.Layout+"\" for "+c.Version))
			continue
		}
		if c.Rate == 0 {
			rejects = append(rejects, WrapMsg(ErrIllegalField, c.Asset, c.Track, StageMetadata, -1,
				"zero sample rate for "+c.Version))
			continue
		}
		specs = append(specs, AudioSpec{Rate: c.Rate, Layout: c.Layout, Channels: ch})
	}
	if len(specs) == 0 {
		return nil, rejects
	}
	eff := target
	if !explicit || eff.Rate == 0 || eff.Layout == "" {
		eff = specs[0] // native fallback
	} else if ch, ok := KnownLayouts[eff.Layout]; ok {
		eff.Channels = ch
	}
	divergent := false
	for _, s := range specs {
		if s != specs[0] {
			divergent = true
			break
		}
	}
	if divergent && !explicit {
		rejects = append(rejects, WrapMsg(ErrConflict, asset, cands[0].Track, StageMetadata, -1,
			"audio layout/rate mismatch without an explicit target configuration"))
		return nil, rejects
	}
	dec := &AudioDecision{Target: eff, Explicit: explicit}
	for i, s := range specs {
		if s != eff {
			dec.Conversions = append(dec.Conversions, AudioConversion{
				Source: cands[i].SourceMeta, From: s, To: eff})
		}
	}
	return dec, rejects
}

// ColorCandidate is one version's NCLX color metadata with explicit priority.
type ColorCandidate struct {
	SourceMeta
	Prio  uint32
	Color ColorInfo
}

// ColorResolution records the winning value and every discarded alternative.
type ColorResolution struct {
	Picked  *ColorCandidate
	Dropped []ColorCandidate
	Missing bool // no candidate carried usable color metadata
}

// ResolveColor applies explicit-priority arbitration to conflicting color
// metadata. Candidates are first collapsed per (asset, version) so multiple
// fragments of one version cast a single vote; equal values merge silently; a
// missing set is flagged rather than invented. Ties keep the first candidate.
func ResolveColor(cands []ColorCandidate) ColorResolution {
	usable := make([]ColorCandidate, 0, len(cands))
	seen := map[SourceMeta]bool{}
	for _, c := range cands {
		if !c.Color.Present {
			continue
		}
		if seen[c.SourceMeta] {
			continue
		}
		seen[c.SourceMeta] = true
		usable = append(usable, c)
	}
	if len(usable) == 0 {
		return ColorResolution{Missing: true}
	}
	win := 0
	conflict := false
	for i := 1; i < len(usable); i++ {
		if !sameColor(usable[i].Color, usable[0].Color) {
			conflict = true
		}
		if usable[i].Prio > usable[win].Prio {
			win = i
		}
	}
	out := ColorResolution{Picked: &usable[win]}
	if conflict {
		for i, c := range usable {
			if i != win {
				out.Dropped = append(out.Dropped, c)
			}
		}
		sort.SliceStable(out.Dropped, func(i, j int) bool {
			if out.Dropped[i].Prio != out.Dropped[j].Prio {
				return out.Dropped[i].Prio > out.Dropped[j].Prio
			}
			return out.Dropped[i].Version < out.Dropped[j].Version
		})
	}
	return out
}

func sameColor(a, b ColorInfo) bool {
	return a.Primaries == b.Primaries && a.Transfer == b.Transfer &&
		a.Matrix == b.Matrix && a.FullRange == b.FullRange
}
