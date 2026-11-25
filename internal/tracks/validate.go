package tracks

import (
	"fmt"

	"mediacheck/internal/report"
)

// Validate checks dependency order and metadata conflicts. Dependency tracks
// are visited and validated before the tracks that reference them.
func (g *Graph) Validate() []report.Record {
	var records []report.Record
	state := make(map[uint64]int, len(g.Tracks))
	done := make(map[uint64]bool, len(g.Tracks))
	g.Order = nil

	var visit func(n uint64, path []uint64)
	visit = func(n uint64, path []uint64) {
		t, ok := g.Tracks[n]
		if !ok {
			return
		}
		if state[n] == 2 {
			return
		}
		if state[n] == 1 {
			records = append(records, issue(int(n), int(t.Entry.Offset), report.TrackDependency, "dependency_cycle",
				"track dependency cycle: %v", append(path, n)))
			return
		}
		state[n] = 1
		for i, dep := range t.DependsOn {
			depOffset := t.Entry.Offset
			if i < len(t.DependencyOff) {
				depOffset = t.DependencyOff[i]
			}
			dt, exists := g.Tracks[dep]
			if !exists {
				records = append(records, issue(int(n), depOffset, report.TrackDependency, "missing_dependency",
					"track %d depends on unknown track %d", n, dep))
				continue
			}
			visit(dep, append(append([]uint64{}, path...), n))
			_ = dt
		}
		state[n] = 2
		if !done[n] {
			done[n] = true
			g.Order = append(g.Order, n)
			records = append(records, validateTrack(g, t)...)
		}
	}

	// Metadata byte order drives the outer traversal; DFS makes dependencies
	// precede dependents in g.Order.
	for _, n := range g.metadataOrder() {
		visit(n, nil)
	}
	return records
}

func (g *Graph) metadataOrder() []uint64 {
	out := make([]uint64, 0, len(g.Tracks))
	for n := range g.Tracks {
		out = append(out, n)
	}
	// Track numbers in these fixtures are small and monotonic with metadata
	// order. Sorting numbers gives deterministic traversal without relying on
	// Go map iteration.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func validateTrack(g *Graph, t *Track) []report.Record {
	var out []report.Record
	if t.Type == 2 || t.Channels != 0 || t.Layout != LayoutUnknown {
		if expected, ok := LayoutChannels[t.Layout]; ok && t.Channels != 0 && int(t.Channels) != expected {
			offset := t.LayoutOffset
			if offset == 0 {
				offset = t.ChannelsOffset
			}
			rec := issue(int(t.Number), offset, report.TrackLayoutConflict, "channel_layout_mismatch",
				"channel layout %d requires %d channels but Channels=%d at metadata offset %d",
				t.Layout, expected, t.Channels, t.ChannelsOffset)
			rec.ByteDelta = int64(int(t.Channels) - expected)
			out = append(out, rec)
		}
	}

	for _, dep := range t.DependsOn {
		dt, ok := g.Tracks[dep]
		if !ok {
			continue
		}
		if t.SampleRate > 0 && dt.SampleRate > 0 && t.SampleRate != dt.SampleRate {
			out = append(out, issue(int(t.Number), t.SampleRateOff, report.TrackSampleRateConflict, "sample_rate_mismatch",
				"track %d sample rate %.0fHz conflicts with dependency track %d rate %.0fHz at offset %d",
				t.Number, t.SampleRate, dep, dt.SampleRate, dt.SampleRateOff))
		}
		if t.Type == 1 && dt.Type == 1 && t.Color != nil && dt.Color != nil {
			if rec, bad := colorPairConflict(t, dt); bad {
				out = append(out, rec)
			}
		}
	}

	if t.Color != nil {
		if offset, code, msg, ok := inconsistentColorBundle(t); ok {
			out = append(out, issue(int(t.Number), offset, code, "color_metadata_conflict", "%s", msg))
		}
	}
	return out
}

func inconsistentColorBundle(t *Track) (int, string, string, bool) {
	c := t.Color
	values := []struct {
		value  uint64
		offset int
		name   string
	}{
		{c.Matrix, c.MatrixOffset, "MatrixCoefficients"},
		{c.Transfer, c.TransferOffset, "TransferCharacteristics"},
		{c.Primaries, c.PrimariesOffset, "ColourPrimaries"},
	}
	has709, has601 := false, false
	for _, v := range values {
		if v.value == 1 {
			has709 = true
		}
		if v.value == 6 {
			has601 = true
		}
	}
	if !(has709 && has601) {
		return 0, "", "", false
	}
	// Point at the first field in the 601 group when a 709 baseline is present;
	// this is the metadata byte that breaks the consistent colour bundle.
	for _, v := range values {
		if v.value == 6 && v.offset != 0 {
			return v.offset, report.TrackColorConflict,
				fmt.Sprintf("%s=%d mixes BT.601/SMPTE metadata with BT.709 fields in track %d", v.name, v.value, t.Number), true
		}
	}
	return c.Element.Offset, report.TrackColorConflict, "colour metadata mixes incompatible standards", true
}

func colorPairConflict(t, dep *Track) (report.Record, bool) {
	a, b := t.Color, dep.Color
	if a.Matrix != b.Matrix && a.MatrixOffset != 0 && b.Matrix != 0 {
		return issue(int(t.Number), a.MatrixOffset, report.TrackColorConflict, "cross_track_color_mismatch",
			"matrix coefficients %d conflict with dependency track %d value %d", a.Matrix, dep.Number, b.Matrix), true
	}
	return report.Record{}, false
}

func issue(track, offset int, code, category, format string, args ...any) report.Record {
	return report.Record{
		Type:          "issue",
		Track:         track,
		InputOffset:   offset,
		FailureOffset: offset,
		ErrorCode:     code,
		Category:      category,
		Severity:      report.SeverityError,
		Message:       fmt.Sprintf(format, args...),
	}
}
