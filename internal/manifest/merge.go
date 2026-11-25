package manifest

import (
	"sort"

	"github.com/zld/media-check/internal/diagnostic"
	"github.com/zld/media-check/internal/export"
	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/timeline"
)

var stageOrder = []string{
	"ingest", "mp4", "parse", "scan", "timeline", "diagnostic", "export", "fingerprint", "manifest",
}

// Merge deterministically unions two manifests for the same stable task key.
// Repeating the same incoming manifest does not append duplicate records.
func Merge(existing, next *Manifest) *Manifest {
	if existing == nil {
		return normalize(next)
	}
	if next == nil {
		return normalize(existing)
	}
	out := &Manifest{
		SchemaVersion:   SchemaVersion,
		TaskKey:         firstNonEmpty(next.TaskKey, existing.TaskKey),
		Scope:           firstNonEmpty(next.Scope, existing.Scope),
		Corruption:      firstNonEmpty(next.Corruption, existing.Corruption),
		Summary:         next.Summary,
		Tracks:          next.Tracks,
		DuplicateGroups: next.DuplicateGroups,
		Export:          next.Export,
		Checkpoint:      next.Checkpoint,
	}
	if next.Diagnostics != nil {
		out.Diagnostics = mergeDiagnostics(existing.Diagnostics, next.Diagnostics)
	} else {
		out.Diagnostics = existing.Diagnostics
	}
	out.Inputs = mergeInputs(existing.Inputs, next.Inputs)
	out.FingerprintGroups = mergeFingerprintGroups(existing.FingerprintGroups, next.FingerprintGroups)
	out.Issues = mergeIssues(existing.Issues, next.Issues)
	out.Stages = mergeStages(existing.Stages, next.Stages)
	return normalize(out)
}

func normalize(m *Manifest) *Manifest {
	if m == nil {
		m = &Manifest{}
	}
	if m.SchemaVersion == 0 {
		m.SchemaVersion = SchemaVersion
	}
	if m.Inputs == nil {
		m.Inputs = []InputRecord{}
	}
	if m.Stages == nil {
		m.Stages = []string{}
	}
	if m.FingerprintGroups == nil {
		m.FingerprintGroups = []FingerprintGroup{}
	}
	if m.DuplicateGroups == nil {
		m.DuplicateGroups = []timeline.DuplicateGroup{}
	}
	sort.Slice(m.Inputs, func(i, j int) bool { return m.Inputs[i].ID < m.Inputs[j].ID })
	sort.Slice(m.FingerprintGroups, func(i, j int) bool {
		return m.FingerprintGroups[i].ContentSHA256 < m.FingerprintGroups[j].ContentSHA256
	})
	for i := range m.FingerprintGroups {
		refs := m.FingerprintGroups[i].TimelineRefs
		sort.Slice(refs, func(a, b int) bool { return refs[a].SegmentID < refs[b].SegmentID })
		m.FingerprintGroups[i].TimelineRefs = compactRefs(refs)
		m.FingerprintGroups[i].Count = len(m.FingerprintGroups[i].TimelineRefs)
	}
	return m
}

func mergeInputs(a, b []InputRecord) []InputRecord {
	byID := map[string]InputRecord{}
	for _, in := range append(append([]InputRecord{}, a...), b...) {
		byID[in.ID] = in
	}
	out := make([]InputRecord, 0, len(byID))
	for _, in := range byID {
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func BuildFingerprintGroups(segments []export.SegmentStat) []FingerprintGroup {
	byHash := map[string]*FingerprintGroup{}
	for _, seg := range segments {
		key := seg.MaterialSHA256
		g, ok := byHash[key]
		if !ok {
			g = &FingerprintGroup{Algorithm: "sha256", ContentSHA256: key, MaterialID: seg.MaterialID}
			byHash[key] = g
		}
		g.TimelineRefs = append(g.TimelineRefs, TimelineRef{
			SegmentID: seg.SegmentID, TrackID: seg.TrackID,
			StartMS: seg.TimelineStartMS, EndMS: seg.TimelineEndMS,
		})
	}
	out := make([]FingerprintGroup, 0, len(byHash))
	for _, g := range byHash {
		g.TimelineRefs = compactRefs(g.TimelineRefs)
		g.Count = len(g.TimelineRefs)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ContentSHA256 < out[j].ContentSHA256 })
	return out
}

func mergeFingerprintGroups(a, b []FingerprintGroup) []FingerprintGroup {
	byHash := map[string]*FingerprintGroup{}
	for _, g := range append(append([]FingerprintGroup{}, a...), b...) {
		key := g.ContentSHA256
		cur, ok := byHash[key]
		if !ok {
			g := g
			byHash[key] = &g
			continue
		}
		cur.TimelineRefs = append(cur.TimelineRefs, g.TimelineRefs...)
		if cur.MaterialID == "" {
			cur.MaterialID = g.MaterialID
		}
	}
	out := make([]FingerprintGroup, 0, len(byHash))
	for _, g := range byHash {
		g.TimelineRefs = compactRefs(g.TimelineRefs)
		g.Count = len(g.TimelineRefs)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ContentSHA256 < out[j].ContentSHA256 })
	return out
}

func compactRefs(in []TimelineRef) []TimelineRef {
	seen := map[string]TimelineRef{}
	for _, ref := range in {
		seen[ref.SegmentID] = ref
	}
	out := make([]TimelineRef, 0, len(seen))
	for _, ref := range seen {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SegmentID < out[j].SegmentID })
	return out
}

func mergeIssues(a, b []*fault.Error) []*fault.Error {
	seen := map[string]*fault.Error{}
	for _, e := range append(append([]*fault.Error{}, a...), b...) {
		if e == nil {
			continue
		}
		key := string(e.Stage) + "|" + string(e.Code) + "|" + e.Message
		seen[key] = e
	}
	out := make([]*fault.Error, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Offset != out[j].Offset {
			return out[i].Offset < out[j].Offset
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func mergeStages(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	rank := map[string]int{}
	for i, s := range stageOrder {
		rank[s] = i
	}
	sort.Slice(out, func(i, j int) bool {
		ri, oki := rank[out[i]]
		rj, okj := rank[out[j]]
		if oki && okj && ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}

func mergeDiagnostics(a, b *diagnostic.Report) *diagnostic.Report {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	seen := map[string]diagnostic.Segment{}
	for _, s := range append(append([]diagnostic.Segment{}, a.Segments...), b.Segments...) {
		key := s.Kind + "|" + itoa(int(s.TrackID)) + "|" + itoa(int(s.StartMS)) + "|" + itoa(int(s.EndMS)) + "|" + itoa(int(s.FirstBadOffset))
		seen[key] = s
	}
	segments := make([]diagnostic.Segment, 0, len(seen))
	for _, s := range seen {
		segments = append(segments, s)
	}
	sort.Slice(segments, func(i, j int) bool {
		if segments[i].FirstBadOffset != segments[j].FirstBadOffset {
			return segments[i].FirstBadOffset < segments[j].FirstBadOffset
		}
		return segments[i].ID < segments[j].ID
	})
	return &diagnostic.Report{
		DamageCount:    len(segments),
		FirstBadOffset: b.FirstBadOffset,
		TrackID:        b.TrackID,
		StartMS:        b.StartMS,
		EndMS:          b.EndMS,
		Segments:       segments,
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		pos--
		buf[pos] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
