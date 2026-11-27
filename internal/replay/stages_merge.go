package replay

import (
	"path/filepath"
	"sort"

	"replay/internal/fault"
	"replay/internal/graph"
	"replay/internal/manifest"
	"replay/internal/merge"
)

// runMerge publishes the plan. Conflicts are carried as counts, not
// failures: a conflict is reported data, while an unresolved merge error
// fails the stage and publishes nothing.
func (e *Engine) runMerge() (string, int, string, []string, error) {
	if e.mapArt == nil || e.graphArt == nil {
		if err := e.loadArtifacts(); err != nil {
			return "", 0, "", nil, stageError(StageMerge, err)
		}
	}
	g, err := graph.New(e.Fixture.Tracks)
	if err != nil {
		return "", 0, "", nil, stageError(StageMerge, err)
	}

	inputs := make([]merge.MergeInput, 0, len(e.mapArt.Tracks))
	for _, tm := range e.mapArt.Tracks {
		inputs = append(inputs, merge.MergeInput{Track: tm.Track, Result: tm.Result})
	}
	plan, err := merge.Merge(g, inputs)
	if err != nil {
		return "", 0, "", nil, stageError(StageMerge, err)
	}

	outDir := filepath.Join(e.WorkDir, "out")
	path, digest, err := plan.Write(outDir)
	if err != nil {
		return "", 0, "", nil, stageError(StageMerge, err)
	}
	rel, err := filepath.Rel(e.WorkDir, path)
	if err != nil {
		return "", 0, "", nil, fault.New(fault.KindInput, "cannot relativise plan path").
			At(StageMerge).Wrap(err)
	}
	segments := len(plan.Segments)
	e.plan = &planRef{path: filepath.ToSlash(rel), digest: digest, plan: plan}
	return digest, segments, "segments", []string{filepath.ToSlash(rel)}, nil
}

// runManifest hashes every published artefact, writes the manifest, then
// verifies it end to end.
func (e *Engine) runManifest() (string, int, string, []string, error) {
	if e.mapArt == nil || e.plan == nil {
		if err := e.loadArtifacts(); err != nil {
			return "", 0, "", nil, stageError(StageManifest, err)
		}
	}
	if e.plan == nil {
		return "", 0, "", nil, fault.New(fault.KindInput, "merge plan is missing").
			At(StageManifest)
	}

	entries, err := e.collectEntries()
	if err != nil {
		return "", 0, "", nil, stageError(StageManifest, err)
	}
	sources := e.collectSources()
	stats := e.collectStats()
	stages := e.stageHashes(StageManifest)

	doc, err := manifest.Build(e.doc.RunID, e.Fixture.Case, e.Fixture.Versions, entries, sources, stages, stats)
	if err != nil {
		return "", 0, "", nil, stageError(StageManifest, err)
	}
	if _, _, err := doc.Write(e.WorkDir); err != nil {
		return "", 0, "", nil, stageError(StageManifest, err)
	}
	if bad, err := manifest.Verify(e.WorkDir, doc); err != nil {
		return "", 0, "", nil, fault.New(fault.KindManifest,
			"manifest verification failed: "+joinLimited(bad)).
			At(StageManifest).Wrap(err)
	}

	e.finalManifest = doc
	return doc.ArtifactHash, len(entries), "artefacts", []string{fileManifest}, nil
}

// collectEntries hashes every published artefact: generated assets, the
// plan and the manifest's own source files. Checkpoint and baseline files
// are deliberately excluded, since they change on every run and are not
// media outputs.
func (e *Engine) collectEntries() ([]manifest.Entry, error) {
	var entries []manifest.Entry
	for _, tm := range e.mapArt.Tracks {
		for _, record := range tm.Assets {
			entry, err := manifest.EntryFor(e.WorkDir, e.path(record.Path), "asset")
			if err != nil {
				return nil, err
			}
			entries = append(entries, entry)
		}
	}
	for _, item := range []struct {
		path string
		kind string
	}{
		{e.plan.path, "plan"},
		{fileMap, "map"},
		{fileTimeline, "timeline"},
	} {
		if !fileExists(e.path(item.path)) {
			continue
		}
		entry, err := manifest.EntryFor(e.WorkDir, e.path(item.path), item.kind)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (e *Engine) collectSources() []manifest.Source {
	var sources []manifest.Source
	for _, tm := range e.mapArt.Tracks {
		for _, slot := range tm.Result.Slots {
			sources = append(sources, manifest.Source{
				Path:        filepath.ToSlash(filepath.Join("assets", slot.Track+"-"+slot.Version+".mp4")),
				Track:       slot.Track,
				Version:     slot.Version,
				IntervalNS:  [2]int64{slot.Interval.Start, slot.Interval.End},
				InputDigest: slot.Digest,
			})
		}
	}
	sort.SliceStable(sources, func(i, j int) bool {
		if sources[i].Track != sources[j].Track {
			return sources[i].Track < sources[j].Track
		}
		return sources[i].IntervalNS[0] < sources[j].IntervalNS[0]
	})
	return sources
}

func (e *Engine) collectStats() manifest.Stats {
	stats := manifest.Stats{}
	for _, stream := range e.ingestArt.Streams {
		stats.Packets += len(stream.Packets)
		stats.Lost += stream.GapCount()
		stats.DuplicateCount += stream.DuplicateCount()
		stats.Wraps += stream.Wraps
	}
	stats.Tracks = len(e.ingestArt.Streams)
	stats.Missing = e.mapArt.Missing
	stats.Conflicts = e.mapArt.Conflicts
	return stats
}

func (e *Engine) stageHashes(upTo string) []manifest.StageHash {
	var out []manifest.StageHash
	for _, stage := range Plan() {
		if stage == upTo {
			break
		}
		cp, ok := e.doc.Stage(stage)
		if !ok {
			continue
		}
		out = append(out, manifest.StageHash{
			Stage:      stage,
			InputHash:  cp.InputHash,
			OutputHash: cp.OutputHash,
		})
	}
	return out
}

func joinLimited(parts []string) string {
	if len(parts) > 5 {
		parts = append(parts[:5:5], "...")
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
