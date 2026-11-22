// Package audio recovers channel layout and sample-rate agreement for
// late-arriving audio tracks, and reconciles frame gaps, overlaps and a
// final partial frame.
package audio

import (
	"fmt"
	"sort"
	"strings"

	"example.com/mediatimeline/internal/model"
)

// Layout is a canonical channel layout with an explicit channel order.
type Layout struct {
	Name     string   `json:"name"`
	Channels []string `json:"channels"`
}

// Canonical layouts. Channel names follow the common WAVE channel mask
// naming so that a mapping between any two of them is unambiguous.
var canonical = []Layout{
	{Name: "mono", Channels: []string{"FC"}},
	{Name: "stereo", Channels: []string{"FL", "FR"}},
	{Name: "2.1", Channels: []string{"FL", "FR", "LFE"}},
	{Name: "3.0", Channels: []string{"FL", "FR", "FC"}},
	{Name: "quad", Channels: []string{"FL", "FR", "BL", "BR"}},
	{Name: "5.0", Channels: []string{"FL", "FR", "FC", "BL", "BR"}},
	{Name: "5.1", Channels: []string{"FL", "FR", "FC", "LFE", "BL", "BR"}},
	{Name: "5.1(side)", Channels: []string{"FL", "FR", "FC", "LFE", "SL", "SR"}},
	{Name: "7.1", Channels: []string{"FL", "FR", "FC", "LFE", "BL", "BR", "SL", "SR"}},
}

// Lookup returns the canonical layout with the given name.
func Lookup(name string) (Layout, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, l := range canonical {
		if l.Name == n {
			return l, true
		}
	}
	return Layout{}, false
}

// ForChannels returns the canonical layout with exactly n channels, if one
// is unambiguous. 2 channels is stereo; 6 is 5.1; 8 is 7.1.
func ForChannels(n int) (Layout, bool) {
	switch n {
	case 1:
		return Lookup("mono")
	case 2:
		return Lookup("stereo")
	case 6:
		return Lookup("5.1")
	case 8:
		return Lookup("7.1")
	default:
		return Layout{}, false
	}
}

// Mapping is a plan that converts source channels into an explicit target
// order. Mixed[i] is false when target channel i is silent (a dropped
// channel, e.g. LFE folded out).
type Mapping struct {
	From   Layout `json:"from"`
	To     Layout `json:"to"`
	Source []int  `json:"source"` // target index -> source index, -1 for silence
	Mixed  []bool `json:"mixed"`
	Reason string `json:"reason"`
}

// Plan computes the explicit mapping between two layouts. It never guesses:
// if a target channel has no counterpart, it reports ErrIncompatible rather
// than silently dropping or duplicating audio.
func Plan(from, to Layout) (Mapping, error) {
	m := Mapping{From: from, To: to, Source: make([]int, len(to.Channels)), Mixed: make([]bool, len(to.Channels))}
	if len(from.Channels) == 0 || len(to.Channels) == 0 {
		return m, fmt.Errorf("empty channel layout")
	}
	index := make(map[string]int, len(from.Channels))
	for i, name := range from.Channels {
		index[name] = i
	}
	missing := make([]string, 0, len(to.Channels))
	for i, name := range to.Channels {
		if src, ok := index[name]; ok {
			m.Source[i] = src
			m.Mixed[i] = true
			continue
		}
		// A back channel can be fed from the matching side channel when the
		// target uses one convention and the source the other. The mapping is
		// explicitly recorded as a remap rather than a silent substitution.
		if alt, ok := counterpart(name); ok {
			if src, ok := index[alt]; ok {
				m.Source[i] = src
				m.Mixed[i] = true
				m.Reason = appendReason(m.Reason, fmt.Sprintf("%s remapped from %s", name, alt))
				continue
			}
		}
		m.Source[i] = -1
		missing = append(missing, name)
	}
	if len(missing) > 0 {
		return m, fmt.Errorf("no source channel for %s", strings.Join(missing, ", "))
	}
	if m.Reason == "" {
		m.Reason = "identity channel order"
	}
	return m, nil
}

func counterpart(name string) (string, bool) {
	switch name {
	case "BL":
		return "SL", true
	case "BR":
		return "SR", true
	case "SL":
		return "BL", true
	case "SR":
		return "BR", true
	default:
		return "", false
	}
}

// sourceRank orders declaration sources by precedence: the container's own
// sample entry wins over a bitstream tag, which wins over an external manifest.
func sourceRank(source string) int {
	switch {
	case strings.HasPrefix(source, "mp4"), source == "mp4-stsd":
		return 0
	case strings.HasPrefix(source, "sei"), strings.HasPrefix(source, "bitstream"):
		return 1
	default:
		return 2
	}
}

func appendReason(cur, add string) string {
	if cur == "" {
		return add
	}
	return cur + "; " + add
}

// Resolve picks the target layout from competing declarations. It prefers an
// explicit layout name, then the declared channel count, and records every
// disagreement as a recoverable error with the decision it made.
func Resolve(track model.TrackID, decls []model.AudioDescriptor) (Layout, Mapping, []*model.Error, error) {
	var errs []*model.Error
	if len(decls) == 0 {
		e := model.NewError(track, model.StageAudio, model.CodeAudioLayout, false,
			"no audio layout declarations")
		return Layout{}, Mapping{}, append(errs, e), e
	}
	// Deterministic order regardless of input order.
	sorted := append([]model.AudioDescriptor(nil), decls...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, rj := sourceRank(sorted[i].Source), sourceRank(sorted[j].Source)
		if ri != rj {
			return ri < rj
		}
		if sorted[i].Source != sorted[j].Source {
			return sorted[i].Source < sorted[j].Source
		}
		return sorted[i].Layout < sorted[j].Layout
	})

	// Precedence order: explicit layout names sorted by source rank
	// (container first, then bitstream, then manifest), so the pick is stable
	// regardless of declaration order.
	target := Layout{}
	found := false
	for _, d := range sorted {
		if d.Layout == "" {
			continue
		}
		l, ok := Lookup(d.Layout)
		if !ok {
			errs = append(errs, model.NewError(track, model.StageAudio, model.CodeAudioLayout, true,
				"declaration %q names unknown layout %q; ignored", d.Source, d.Layout))
			continue
		}
		if !found {
			target, found = l, true
			continue
		}
		if l.Name != target.Name {
			errs = append(errs, model.NewError(track, model.StageAudio, model.CodeAudioLayout, true,
				"layout conflict: %q says %s, keeping %s", d.Source, l.Name, target.Name))
		}
	}
	if !found {
		counts := map[int][]string{}
		for _, d := range sorted {
			if d.ChannelCount > 0 {
				counts[d.ChannelCount] = append(counts[d.ChannelCount], d.Source)
			}
		}
		if len(counts) > 1 {
			errs = append(errs, model.NewError(track, model.StageAudio, model.CodeAudioLayout, true,
				"channel count conflict across %d declarations; choosing the smallest", len(counts)))
		}
		countList := make([]int, 0, len(counts))
		for n := range counts {
			countList = append(countList, n)
		}
		sort.Ints(countList)
		if len(countList) > 0 {
			if l, ok := ForChannels(countList[0]); ok {
				target, found = l, true
			}
		}
	}
	if !found {
		src := sorted[0]
		e := model.NewError(track, model.StageAudio, model.CodeAudioLayout, false,
			"cannot determine a canonical layout from %q (%d channels)", src.Source, src.ChannelCount)
		return Layout{}, Mapping{}, append(errs, e), e
	}

	// Map every declaration into the target. A declaration whose layout is
	// incompatible is recovered only through an explicit mapping; if none
	// exists the layer is unavailable.
	var mapping Mapping
	first := true
	for _, d := range sorted {
		from, ok := Layout{}, false
		if d.Layout != "" {
			from, ok = Lookup(d.Layout)
		} else if d.ChannelCount > 0 {
			from, ok = ForChannels(d.ChannelCount)
		}
		if !ok {
			continue
		}
		// The declaration that selected the target defines the mapping; other
		// declarations are only checked for compatibility with it.
		m, err := Plan(from, target)
		if err != nil {
			errs = append(errs, model.NewError(track, model.StageAudio, model.CodeAudioLayout, true,
				"declaration %q (%s) cannot map to %s: %v; dropped", d.Source, from.Name, target.Name, err))
			continue
		}
		if first {
			mapping, first = m, false
		} else if !sameMapping(mapping, m) && d.Layout != "" {
			errs = append(errs, model.NewError(track, model.StageAudio, model.CodeAudioLayout, true,
				"declaration %q maps differently (%s); using the first explicit mapping", d.Source, m.Reason))
		}
	}
	if first {
		e := model.NewError(track, model.StageAudio, model.CodeAudioLayout, false,
			"no declaration could be mapped into %s", target.Name)
		return target, Mapping{}, append(errs, e), e
	}
	mapping.Reason = fmt.Sprintf("%s -> %s: %s", mapping.From.Name, mapping.To.Name, mapping.Reason)
	return target, mapping, errs, nil
}

func sameMapping(a, b Mapping) bool {
	if a.From.Name != b.From.Name || a.To.Name != b.To.Name || len(a.Source) != len(b.Source) {
		return false
	}
	for i := range a.Source {
		if a.Source[i] != b.Source[i] || a.Mixed[i] != b.Mixed[i] {
			return false
		}
	}
	return true
}
