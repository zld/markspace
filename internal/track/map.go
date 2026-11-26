// Package track maps media tracks to output roles under explicit rules. It
// never guesses: an unspecified track is an error, a duplicate destination is
// an error, and a channel-count conflict is an error.
package track

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"liveregress/internal/clock"
	"liveregress/internal/fault"
)

// Role is the destination of a mapping, e.g. "a1", "a2", "v0".
type Role string

// Source describes an input track discovered by a demuxer.
type Source struct {
	// ID is the container track/track-number. Zero is not valid.
	ID uint32
	// Name is an optional human label, e.g. "center".
	Name string
	// Channels is the declared channel count; 0 means unknown.
	Channels int
	// SampleRate is the declared rate; 0 means unknown.
	SampleRate int
	// Kind is "audio", "video" or "iframes".
	Kind string
	// Layer is the representation layer this source belongs to.
	Layer string
	// Duration is the exact declared duration, if known.
	Duration clock.Rational
	// Path is the source file, used for diagnostics.
	Path string
}

// Rule assigns one source track to one role, with an explicit channel map.
type Rule struct {
	// SourceID selects Source.ID exactly. Required.
	SourceID uint32
	// Layer narrows the source to a representation layer. Empty means the
	// rule is ambiguous when more than one layer exposes SourceID.
	Layer string
	// Role is the destination role. Required and unique per lane.
	Role Role
	// ChannelMap lists, for each destination channel, the zero-based source
	// channel. For a three-channel WAV -> three-channel AAC track, this is
	// [0,1,2]. A stereo destination from three channels may be [0,2].
	ChannelMap []int
	// ExpectChannels, when non-zero, is the mandated destination width and
	// must equal len(ChannelMap).
	ExpectChannels int
}

// Mapping is a resolved, validated rule.
type Mapping struct {
	Source     Source
	Role       Role
	ChannelMap []int
	// OutputChannels is len(ChannelMap).
	OutputChannels int
}

// Plan is the full, validated mapping for one run.
type Plan struct {
	Mappings []Mapping
}

// Roles returns the mapped roles in stable order.
func (p *Plan) Roles() []Role {
	out := make([]Role, 0, len(p.Mappings))
	for _, m := range p.Mappings {
		out = append(out, m.Role)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Build resolves rules against sources. Every source must be claimed exactly
// once and every role must be produced exactly once.
func Build(sources []Source, rules []Rule) (*Plan, error) {
	if len(rules) == 0 {
		return nil, fault.New(fault.KindMapping, "", -1, "no mapping rules supplied")
	}
	usedRole := make(map[Role]bool, len(rules))
	claimed := make(map[[2]string]bool, len(rules)) // layer+id
	plan := &Plan{}
	for _, rule := range rules {
		if rule.SourceID == 0 {
			return nil, fault.New(fault.KindMapping, "", -1, "mapping rule has no explicit source track id")
		}
		if rule.Role == "" {
			return nil, fault.New(fault.KindMapping, "", -1,
				fmt.Sprintf("source track %d maps to an empty role", rule.SourceID)).
				WithTrack(rule.SourceID, "")
		}
		if usedRole[rule.Role] {
			return nil, fault.New(fault.KindMapping, "", -1,
				fmt.Sprintf("duplicate mapping to role %q", rule.Role)).
				WithTrack(rule.SourceID, "")
		}
		usedRole[rule.Role] = true

		src, err := selectSource(sources, rule)
		if err != nil {
			return nil, err
		}
		key := [2]string{src.Layer, fmt.Sprint(src.ID)}
		if claimed[key] {
			return nil, fault.New(fault.KindMapping, src.Path, -1,
				fmt.Sprintf("source track %d in layer %q mapped more than once", src.ID, src.Layer)).
				WithTrack(src.ID, src.Name).InLayer(src.Layer)
		}
		claimed[key] = true

		chMap, err := resolveChannels(src, rule)
		if err != nil {
			return nil, err
		}
		plan.Mappings = append(plan.Mappings, Mapping{
			Source: src, Role: rule.Role, ChannelMap: chMap, OutputChannels: len(chMap),
		})
	}

	// Every discovered track must be accounted for: unmapped tracks are an
	// error rather than something to silently drop or substitute.
	for _, s := range sources {
		if !claimed[[2]string{s.Layer, fmt.Sprint(s.ID)}] {
			return nil, fault.New(fault.KindMapping, s.Path, -1,
				fmt.Sprintf("track %d in layer %q has no explicit mapping", s.ID, s.Layer)).
				WithTrack(s.ID, s.Name).InLayer(s.Layer)
		}
	}
	sort.Slice(plan.Mappings, func(i, j int) bool {
		a, b := plan.Mappings[i], plan.Mappings[j]
		if a.Source.Layer != b.Source.Layer {
			return a.Source.Layer < b.Source.Layer
		}
		return a.Role < b.Role
	})
	return plan, nil
}

func selectSource(sources []Source, rule Rule) (Source, error) {
	var matches []Source
	for _, s := range sources {
		if s.ID != rule.SourceID {
			continue
		}
		if rule.Layer != "" && s.Layer != rule.Layer {
			continue
		}
		matches = append(matches, s)
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Source{}, fault.New(fault.KindMissing, "", -1,
			fmt.Sprintf("explicit source track %d not found", rule.SourceID)).
			WithTrack(rule.SourceID, "")
	default:
		layers := make([]string, 0, len(matches))
		for _, m := range matches {
			layers = append(layers, m.Layer)
		}
		return Source{}, fault.New(fault.KindMapping, "", -1,
			fmt.Sprintf("source track %d is ambiguous across layers %s; specify a layer",
				rule.SourceID, strings.Join(layers, ","))).
			WithTrack(rule.SourceID, "")
	}
}

func resolveChannels(src Source, rule Rule) ([]int, error) {
	id, name, layer := src.ID, src.Name, src.Layer
	if len(rule.ChannelMap) == 0 {
		return nil, fault.New(fault.KindMapping, src.Path, -1, "empty channel map").
			WithTrack(id, name).InLayer(layer)
	}
	if rule.ExpectChannels != 0 && rule.ExpectChannels != len(rule.ChannelMap) {
		return nil, fault.New(fault.KindMapping, src.Path, -1,
			fmt.Sprintf("expected %d output channels but channel map has %d", rule.ExpectChannels, len(rule.ChannelMap))).
			WithTrack(id, name).InLayer(layer)
	}
	if src.Channels > 0 && len(rule.ChannelMap) > src.Channels {
		return nil, fault.New(fault.KindMapping, src.Path, -1,
			fmt.Sprintf("channel map needs %d channels but source declares %d", len(rule.ChannelMap), src.Channels)).
			WithTrack(id, name).InLayer(layer)
	}
	seen := make(map[int]bool, len(rule.ChannelMap))
	for i, c := range rule.ChannelMap {
		if c < 0 || (src.Channels > 0 && c >= src.Channels) {
			return nil, fault.New(fault.KindMapping, src.Path, -1,
				fmt.Sprintf("channel map destination %d selects source channel %d outside [0,%d)", i, c, src.Channels)).
				WithTrack(id, name).InLayer(layer)
		}
		if seen[c] {
			return nil, fault.New(fault.KindMapping, src.Path, -1,
				fmt.Sprintf("channel map selects source channel %d more than once", c)).
				WithTrack(id, name).InLayer(layer)
		}
		seen[c] = true
	}
	return append([]int(nil), rule.ChannelMap...), nil
}

// ParseChannelMap parses a comma-separated explicit channel map such as
// "0,1,2". Empty input is an error; there is no default.
func ParseChannelMap(s string) ([]int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fault.New(fault.KindMapping, "", -1, "empty channel map")
	}
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	seen := map[int]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fault.New(fault.KindMapping, "", -1,
				fmt.Sprintf("invalid channel index %q", p))
		}
		if n < 0 {
			return nil, fault.New(fault.KindMapping, "", -1, "negative channel index")
		}
		if seen[n] {
			return nil, fault.New(fault.KindMapping, "", -1,
				fmt.Sprintf("channel %d repeated", n))
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
}
