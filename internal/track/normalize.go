package track

import (
	"sort"

	"mediamerge/internal/ingest"
	"mediamerge/internal/model"
)

// Metadata is the normalized view of one logical track across every source
// file that declared it. The chosen source wins by input-file priority
// (earlier files rank higher); conflicting values are recorded, never
// silently discarded.
type Metadata struct {
	ID               uint32            `json:"id"`
	Handler          string            `json:"handler"`
	Timescale        uint32            `json:"timescale"`
	SampleRate       uint32            `json:"sample_rate"`
	TargetSampleRate uint32            `json:"target_sample_rate,omitempty"`
	Channels         uint16            `json:"channels,omitempty"`
	Layout           string            `json:"layout,omitempty"`
	Color            *ingest.ColorMeta `json:"color,omitempty"`
	Source           string            `json:"source"`
	ChannelPlan      *ChannelPlan      `json:"channel_plan,omitempty"`
	Resample         *ResamplePlan     `json:"resample,omitempty"`
	ColorOverrides   []ColorOverride   `json:"color_overrides,omitempty"`
	Mixable          bool              `json:"mixable"`
	Conflicts        []string          `json:"conflicts,omitempty"`
}

// ChannelPlan documents an explicit channel mapping. No implicit up/downmix
// is ever applied: when no matrix is defined the original layout is kept and
// the track is marked not mixable.
type ChannelPlan struct {
	SourceChannels uint16      `json:"source_channels"`
	TargetChannels uint16      `json:"target_channels"`
	Layout         string      `json:"layout"`
	Mixable        bool        `json:"mixable"`
	Matrix         [][]float64 `json:"matrix,omitempty"`
	Note           string      `json:"note,omitempty"`
}

// ResamplePlan is a rational source->target sample-rate plan. Only the
// arithmetic is planned; no audio is actually resampled or transcoded.
type ResamplePlan struct {
	FromRate      uint32  `json:"from_rate"`
	ToRate        uint32  `json:"to_rate"`
	Up            uint64  `json:"up"`
	Down          uint64  `json:"down"`
	ExactRatio    bool    `json:"exact_ratio"`
	MaxRoundErrNs float64 `json:"max_rounding_error_ns"`
	PlannedOnly   bool    `json:"planned_only"`
}

// FramesFor returns the integer output-frame count for a duration and the
// fractional frame left over by truncation, exposing rounding error.
func (p *ResamplePlan) FramesFor(d model.NsDuration) (int64, float64) {
	if p == nil {
		return 0, 0
	}
	exact := float64(d) * float64(p.ToRate) / float64(model.NsPerSecond)
	frames := int64(exact)
	return frames, exact - float64(frames)
}

// ColorOverride records a lower-priority colour value that lost to the
// chosen source.
type ColorOverride struct {
	Source    string `json:"source"`
	Primaries uint16 `json:"primaries"`
	Transfer  uint16 `json:"transfer"`
	Matrix    uint16 `json:"matrix"`
	FullRange bool   `json:"full_range"`
}

// Normalized is the ordered result of metadata normalization.
type Normalized struct {
	Tracks []Metadata `json:"tracks"`
}

// ByID returns the normalized metadata for a track, or nil.
func (n *Normalized) ByID(id uint32) *Metadata {
	for i := range n.Tracks {
		if n.Tracks[i].ID == id {
			return &n.Tracks[i]
		}
	}
	return nil
}

type candidate struct {
	file     *ingest.File
	meta     *ingest.TrackMeta
	priority int
}

// BuildGraph builds the audio/video dependency graph for the parsed files.
// Every track is registered; each audio track is associated with the
// lowest-id video track from the same upload group so video is normalized
// before audio. Association edges are a synchronization dependency only.
func BuildGraph(files []*ingest.File) *Graph {
	g := NewGraph()
	var video []uint32
	for _, f := range files {
		for _, m := range f.Tracks {
			g.AddTrack(m.ID)
			if m.Handler == "vide" {
				video = append(video, m.ID)
			}
		}
	}
	sort.Slice(video, func(i, j int) bool { return video[i] < video[j] })
	if len(video) == 0 {
		return g
	}
	for _, f := range files {
		for _, m := range f.Tracks {
			if m.Handler == "soun" {
				g.AddEdge(m.ID, video[0])
			}
		}
	}
	return g
}

// Normalize resolves metadata conflicts across files and returns one
// Metadata per logical track, ordered by the dependency graph (topology,
// then track id). A dependency cycle is a fatal structural error.
func Normalize(files []*ingest.File, g *Graph, errs *model.Errors) (*Normalized, *model.Error) {
	byID := make(map[uint32][]candidate)
	var ids []uint32
	for fi, f := range files {
		for i := range f.Tracks {
			m := &f.Tracks[i]
			if _, ok := byID[m.ID]; !ok {
				ids = append(ids, m.ID)
			}
			byID[m.ID] = append(byID[m.ID], candidate{file: f, meta: m, priority: fi})
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	if g == nil {
		g = NewGraph()
	}
	for _, id := range ids {
		g.AddTrack(id)
	}
	if err := g.Validate(); err != nil {
		errs.Add(err)
		return nil, err
	}

	// A single canonical audio rate is chosen as the highest declared rate;
	// each lower rate gets an explicit rational plan towards it.
	var canonicalRate uint32
	for _, id := range ids {
		for _, c := range byID[id] {
			if c.meta.Handler == "soun" && c.meta.SampleRate > canonicalRate {
				canonicalRate = c.meta.SampleRate
			}
		}
	}

	out := &Normalized{}
	for _, id := range topoIDs(g, ids) {
		cands := byID[id]
		sort.SliceStable(cands, func(i, j int) bool { return cands[i].priority < cands[j].priority })
		win := cands[0].meta
		md := Metadata{
			ID:         id,
			Handler:    win.Handler,
			Timescale:  win.Timescale,
			SampleRate: win.SampleRate,
			Channels:   win.Channels,
			Layout:     layoutName(win.Channels),
			Source:     cands[0].file.Path,
			Mixable:    true,
		}
		if md.Handler == "vide" {
			md.Color = chooseColor(id, cands, &md, errs)
		}
		if md.Handler == "soun" {
			normalizeChannels(id, win, cands, &md, errs)
			if canonicalRate > 0 && win.SampleRate > 0 && win.SampleRate != canonicalRate {
				md.TargetSampleRate = canonicalRate
				md.Resample = planResample(win.SampleRate, canonicalRate)
				md.Conflicts = append(md.Conflicts,
					"sources declare different sample rates; resample planned only")
				errs.Add(model.NewError(model.LevelInfo, "normalize", cands[0].file.Path, -1, id,
					"sample rate %d differs from canonical %d: rational plan %d/%d (planned only)",
					win.SampleRate, canonicalRate, md.Resample.Up, md.Resample.Down))
			}
			for _, c := range cands[1:] {
				if c.meta.SampleRate != win.SampleRate || c.meta.Channels != win.Channels {
					errs.Add(model.NewError(model.LevelInfo, "normalize", c.file.Path, -1, id,
						"metadata conflict: sample rate %d/%d channels %d/%d; priority keeps %d/%d",
						win.SampleRate, c.meta.SampleRate, win.Channels, c.meta.Channels,
						win.SampleRate, win.Channels))
				}
			}
		}
		out.Tracks = append(out.Tracks, md)
	}
	return out, nil
}

func topoIDs(g *Graph, ids []uint32) []uint32 {
	ordered := make([]uint32, 0, len(ids))
	seen := make(map[uint32]bool)
	for _, id := range g.TopoOrder() {
		if !seen[id] {
			seen[id] = true
			ordered = append(ordered, id)
		}
	}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			ordered = append(ordered, id)
		}
	}
	return ordered
}

func chooseColor(id uint32, cands []candidate, md *Metadata, errs *model.Errors) *ingest.ColorMeta {
	var chosen *ingest.ColorMeta
	var chosenSrc string
	for _, c := range cands {
		if c.meta.Color == nil {
			continue
		}
		if chosen == nil {
			chosen, chosenSrc = c.meta.Color, c.file.Path
			continue
		}
		if *c.meta.Color != *chosen {
			md.ColorOverrides = append(md.ColorOverrides, ColorOverride{
				Source: c.file.Path, Primaries: c.meta.Color.Primaries,
				Transfer: c.meta.Color.Transfer, Matrix: c.meta.Color.Matrix,
				FullRange: c.meta.Color.FullRange,
			})
			md.Conflicts = append(md.Conflicts,
				"colour metadata overridden from "+c.file.Path)
			errs.Add(model.NewError(model.LevelInfo, "normalize", c.file.Path, -1, id,
				"colour metadata conflict: kept %d/%d/%d from %s, overrode %d/%d/%d",
				chosen.Primaries, chosen.Transfer, chosen.Matrix, chosenSrc,
				c.meta.Color.Primaries, c.meta.Color.Transfer, c.meta.Color.Matrix))
		}
	}
	return chosen
}

func normalizeChannels(id uint32, win *ingest.TrackMeta, cands []candidate, md *Metadata, errs *model.Errors) {
	target := win.Channels
	for _, c := range cands {
		if c.meta.Channels > target {
			target = c.meta.Channels
		}
	}
	plan := &ChannelPlan{
		SourceChannels: win.Channels,
		TargetChannels: target,
		Layout:         layoutName(target),
	}
	if win.Channels == 0 || target == 0 {
		plan.TargetChannels = win.Channels
		plan.Layout = layoutName(win.Channels)
		plan.Note = "unknown channel layout retained"
		md.ChannelPlan = plan
		return
	}
	if win.Channels == target {
		plan.Mixable = true
		plan.Matrix = identity(target)
	} else if m, ok := explicitMatrix(win.Channels, target); ok {
		plan.Mixable = true
		plan.Matrix = m
	} else {
		// No documented matrix: keep the original layout untouched.
		plan.TargetChannels = win.Channels
		plan.Layout = layoutName(win.Channels)
		plan.Mixable = false
		plan.Note = "no explicit matrix for channel-count change; original layout retained, not mixable"
		md.Mixable = false
		md.Conflicts = append(md.Conflicts, plan.Note)
		errs.Add(model.NewError(model.LevelInfo, "normalize", md.Source, -1, id,
			"channel layout %d -> %d has no explicit matrix; original retained, not mixable",
			win.Channels, target))
	}
	md.ChannelPlan = plan
	md.Layout = plan.Layout
}

func layoutName(ch uint16) string {
	switch ch {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	case 0:
		return ""
	default:
		return "custom"
	}
}

func identity(n uint16) [][]float64 {
	m := make([][]float64, n)
	for i := range m {
		m[i] = make([]float64, n)
		m[i][i] = 1
	}
	return m
}

// explicitMatrix contains only hand-written, documented mappings. Anything
// absent means "do not guess".
func explicitMatrix(from, to uint16) ([][]float64, bool) {
	switch {
	case from == 1 && to == 2:
		return [][]float64{{1}, {1}}, true // mono duplicated to L/R
	case from == 2 && to == 1:
		return [][]float64{{0.5, 0.5}}, true // stereo summed at -6 dB
	default:
		return nil, false
	}
}

func planResample(from, to uint32) *ResamplePlan {
	a, b := uint64(from), uint64(to)
	g := gcd(a, b)
	if g == 0 {
		g = 1
	}
	return &ResamplePlan{
		FromRate:      from,
		ToRate:        to,
		Up:            b / g,
		Down:          a / g,
		ExactRatio:    true,
		MaxRoundErrNs: 0.5, // ns-grid rounding of output sample instants
		PlannedOnly:   true,
	}
}

func gcd(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
