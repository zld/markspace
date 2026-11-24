// Package maptrack builds the cross-container track mapping. Several
// containers can hold different versions of the same archived asset; the
// mapper joins them by logical identity, arbitrates their descriptive
// metadata by source priority (MXF > MP4 > default) while keeping every
// original claim, and emits results only in dependency order.
package maptrack

import (
	"fmt"
	"sort"

	"archive/internal/fault"
	"archive/internal/model"
	"archive/internal/probe"
)

// SourcePriority ranks metadata sources. MXF masters win over MP4
// derivatives, and a container default is always last.
func SourcePriority(source string) int {
	switch source {
	case "mxf":
		return 3
	case "mp4":
		return 2
	case "default":
		return 1
	}
	return 0
}

// MappedTrack is the resolved view of one logical track across containers.
type MappedTrack struct {
	Logical  string                 `json:"logical"`
	Kind     model.TrackKind        `json:"kind"`
	Required bool                   `json:"required"`
	Sources  []string               `json:"sources"`
	Timebase model.Timebase         `json:"timebase"`
	StartNS  int64                  `json:"start_ns"`
	EndNS    int64                  `json:"end_ns"`
	Rotation int                    `json:"rotation_deg"`
	Crop     *model.Crop            `json:"crop,omitempty"`
	Color    model.ColorDescription `json:"color"`
	// Members lists the per-container track keys that feed this mapping.
	Members []string `json:"members"`
	// OrderKey is the position in dependency order at which this mapping was
	// emitted; it is monotone and deterministic.
	OrderKey int `json:"order_key"`
	// Dependencies are the logical tracks that had to be mapped first.
	Dependencies []string `json:"dependencies,omitempty"`
}

// Result is the mapper output.
type Result struct {
	Tracks []*MappedTrack `json:"tracks"`
	// Order is the dependency-order list of logical names.
	Order  []string   `json:"order"`
	Faults fault.List `json:"-"`
}

// Options controls mapping behaviour.
type Options struct {
	// RequireTracks lists logical tracks that must be present. A missing
	// required track is a hard failure: no success summary may be emitted.
	RequireTracks []string
	// Dependencies declares logical track dependencies for arbitration and
	// ordering, in addition to those found on the tracks themselves.
	Dependencies map[string][]string
}

// Map joins the probed files, validates their dependency graph and emits the
// mapping in dependency order. It returns a typed fault on a cycle or a
// missing required track so the caller can refuse to summarise success.
func Map(files []*probe.File, opt Options) (*Result, error) {
	if len(files) == 0 {
		return nil, &fault.Error{Code: fault.MissingRequiredTrack, Detail: "map: no input files"}
	}
	// Collect every container track, grouped by logical identity.
	grouped := map[string][]*model.Track{}
	var logicals []string
	for _, f := range files {
		for _, t := range f.Tracks {
			if _, ok := grouped[t.Logical]; !ok {
				logicals = append(logicals, t.Logical)
			}
			grouped[t.Logical] = append(grouped[t.Logical], t)
		}
	}
	sort.Strings(logicals)

	// Validate required tracks before any output is produced.
	for _, req := range opt.RequireTracks {
		if _, ok := grouped[req]; !ok {
			return nil, &fault.Error{
				Code:   fault.MissingRequiredTrack,
				Detail: fmt.Sprintf("map: required logical track %q is absent", req),
			}
		}
	}

	// Build the dependency graph over logical tracks. A dependency that is
	// absent is tolerated only when no track requires it.
	deps := map[string][]string{}
	for _, logical := range logicals {
		members := grouped[logical]
		seen := map[string]bool{}
		for _, m := range members {
			for _, dep := range m.DependsOn {
				if _, ok := grouped[dep]; !ok && m.Required {
					return nil, &fault.Error{
						Code:   fault.MissingRequiredTrack,
						Detail: fmt.Sprintf("map: %s depends on absent track %q", logical, dep),
					}
				}
				if _, ok := grouped[dep]; !ok {
					continue
				}
				if !seen[dep] {
					seen[dep] = true
					deps[logical] = append(deps[logical], dep)
				}
			}
		}
		for _, dep := range opt.Dependencies[logical] {
			if _, ok := grouped[dep]; !ok {
				return nil, &fault.Error{
					Code:   fault.MissingRequiredTrack,
					Detail: fmt.Sprintf("map: %s depends on absent track %q", logical, dep),
				}
			}
			if !seen[dep] {
				seen[dep] = true
				deps[logical] = append(deps[logical], dep)
			}
		}
		sort.Strings(deps[logical])
	}

	order, err := topoOrder(logicals, deps)
	if err != nil {
		return nil, err
	}

	res := &Result{}
	for i, logical := range order {
		mt := resolve(logical, grouped[logical])
		mt.OrderKey = i
		mt.Dependencies = append([]string(nil), deps[logical]...)
		res.Tracks = append(res.Tracks, mt)
		res.Order = append(res.Order, logical)
	}
	return res, nil
}

// topoOrder returns the logical names in dependency order, or a
// dependency_cycle fault if any cycle exists.
func topoOrder(names []string, deps map[string][]string) ([]string, error) {
	state := map[string]uint8{}
	var order []string
	var visit func(n string, stack []string) error
	visit = func(n string, stack []string) error {
		switch state[n] {
		case 2:
			return nil
		case 1:
			return &fault.Error{Code: fault.DependencyCycle, Detail: "map: cycle " + joinCycle(stack, n)}
		}
		state[n] = 1
		for _, dep := range deps[n] {
			if err := visit(dep, append(stack, n)); err != nil {
				return err
			}
		}
		state[n] = 2
		order = append(order, n)
		return nil
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	for _, n := range sorted {
		if err := visit(n, nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func joinCycle(stack []string, last string) string {
	out := ""
	for _, s := range stack {
		out += s + " -> "
	}
	return out + last
}

// resolve merges the container versions of one logical track.
func resolve(logical string, members []*model.Track) *MappedTrack {
	// Deterministic member order: most authoritative source first, then key.
	sorted := append([]*model.Track(nil), members...)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi, pj := SourcePriority(sorted[i].Source), SourcePriority(sorted[j].Source)
		if pi != pj {
			return pi > pj
		}
		return sorted[i].Key < sorted[j].Key
	})
	mt := &MappedTrack{Logical: logical, Kind: sorted[0].Kind}
	for _, m := range sorted {
		mt.Sources = append(mt.Sources, m.Source)
		mt.Members = append(mt.Members, m.Key)
		mt.Required = mt.Required || m.Required
	}
	// Timing is reported on the most authoritative container's clock, which
	// is what a consumer restoring the master needs.
	master := sorted[0]
	mt.Timebase = master.Timebase
	startNS := master.Timebase.TickToNanos(master.StartTick)
	endNS := master.Timebase.TickToNanos(master.StartTick + master.DurationTick)
	if endNS < startNS {
		endNS = startNS
	}
	mt.StartNS, mt.EndNS = startNS, endNS
	// Physical properties are container-local: rotation and crop come from
	// the master and are not averaged across containers.
	mt.Rotation = master.RotationDeg
	mt.Crop = master.Crop
	mt.Color = arbitrate(members)
	return mt
}

// arbitrate resolves the colour description across containers. The winning
// value follows the source priority, but every disagreeing value is kept in
// Conflicts with the field it claimed and the source it came from, so a
// review can see that the MXF and MP4 copies disagreed.
func arbitrate(members []*model.Track) model.ColorDescription {
	type claim struct {
		field  string
		value  string
		source string
		prio   int
	}
	var claimsFor []claim
	add := func(field, value, source string) {
		if value == "" {
			return
		}
		claimsFor = append(claimsFor, claim{field: field, value: value, source: source, prio: SourcePriority(source)})
	}
	for _, m := range members {
		for _, c := range m.ColorClaims {
			field := c.Field
			if field == "" {
				field = guessField(c)
			}
			add(field, c.Value, c.Source)
		}
	}
	fields := []string{"primaries", "transfer", "matrix", "range"}
	desc := model.ColorDescription{ArbitratedBy: "default"}
	var winners []string
	for _, field := range fields {
		var best *claim
		for i := range claimsFor {
			c := &claimsFor[i]
			if c.field != field {
				continue
			}
			if best == nil || c.prio > best.prio {
				best = c
			}
		}
		if best == nil {
			continue
		}
		cf := model.ColorField{Field: field, Value: best.value, Source: best.source}
		switch field {
		case "primaries":
			desc.Primaries = cf
		case "transfer":
			desc.Transfer = cf
		case "matrix":
			desc.Matrix = cf
		case "range":
			desc.Range = cf
		}
		winners = append(winners, best.source)
		// Record every claim that disagrees with the winner, from any
		// source, including equal-value claims from different sources so
		// the provenance is complete.
		for _, c := range claimsFor {
			if c.field != field {
				continue
			}
			if c.value != best.value || c.source != best.source {
				desc.Conflicts = append(desc.Conflicts, model.ColorField{
					Field: field, Value: c.value, Source: c.source,
				})
			}
		}
	}
	if len(winners) > 0 {
		desc.ArbitratedBy = winners[0]
		for _, w := range winners[1:] {
			if SourcePriority(w) < SourcePriority(desc.ArbitratedBy) {
				desc.ArbitratedBy = w
			}
		}
	}
	sort.SliceStable(desc.Conflicts, func(i, j int) bool {
		if desc.Conflicts[i].Field != desc.Conflicts[j].Field {
			return desc.Conflicts[i].Field < desc.Conflicts[j].Field
		}
		return SourcePriority(desc.Conflicts[i].Source) > SourcePriority(desc.Conflicts[j].Source)
	})
	return desc
}

func guessField(c model.ColorField) string {
	if c.Field != "" {
		return c.Field
	}
	return "unknown"
}
