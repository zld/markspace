package pipeline

import (
	"path/filepath"
	"sort"

	"mediad/internal/adapter"
	"mediad/internal/core"
	"mediad/internal/fixture"
	"mediad/internal/journal"
	"mediad/internal/manifest"
	"mediad/internal/model"
	"mediad/internal/provenance"
)

// parseManifest dispatches by the recorded case kind. absPath is used for
// file access; relPath is stored on the returned manifest so downstream
// path joins stay relative to the fixture root.
func parseManifest(kind, absPath, relPath string) (*model.Manifest, *model.SourceError) {
	switch kind {
	case "dash":
		m, err := manifest.ParseDASHFile(absPath)
		if err != nil {
			return nil, mustSE(err)
		}
		m.Path = relPath
		return m, nil
	case "hls":
		m, err := manifest.ParseHLSFile(absPath)
		if err != nil {
			return nil, mustSE(err)
		}
		// variantRel is relative to the master *directory* (which already
		// includes the case name). Prefix the case directory, then resolve
		// each segment URL against the variant playlist directory.
		absMasterDir := filepath.Dir(absPath)
		caseDir := filepath.Base(absMasterDir)
		for i := range m.Representations {
			r := &m.Representations[i]
			variantRel, vErr := filepath.Rel(absMasterDir, r.PlaylistPath)
			if vErr != nil {
				return nil, mustSE(vErr)
			}
			rootRelPlaylist := filepath.Join(caseDir, variantRel)
			variantRelDir := filepath.Dir(rootRelPlaylist)
			for j := range r.Segments {
				r.Segments[j].URL = filepath.Join(variantRelDir, r.Segments[j].URL)
			}
			r.PlaylistPath = rootRelPlaylist
		}
		m.Path = relPath
		return m, nil
	case "rtp":
		return syntheticRTPManifest(relPath)
	default:
		return nil, model.NewError(model.KindUnregisteredParser,
			model.SourceLoc{Manifest: absPath, Entry: -1, Frame: -1},
			"unknown case kind "+kind)
	}
}

// syntheticRTPManifest models a capture as a single-segment representation
// so the downstream processing path is identical to container inputs.
func syntheticRTPManifest(path string) (*model.Manifest, *model.SourceError) {
	rep := model.Representation{
		ID: "rtp-0", Kind: model.TrackAudio, TimeScale: 1,
		Segments: []model.SegmentRef{{
			Index: 0, URL: filepath.Base(path), Start: 0, Duration: 0,
		}},
	}
	return &model.Manifest{
		Kind: "rtp", Path: path, TimeScale: 1,
		Representations: []model.Representation{rep},
	}, nil
}

// processRepresentations runs every representation and merges results into
// the per-case report. It returns false if any representation failed;
// successful representations still emit their full results.
func processRepresentations(root string, mf *model.Manifest, params Parameters,
	cr *CaseResult, builder *provenance.Builder) bool {
	blocked := map[string]bool{}
	for _, ma := range mf.Misaligned {
		blocked[ma.RepA] = true
		blocked[ma.RepB] = true
	}
	chunksByRep := map[string][]core.Chunk{}
	hashSet := map[string]bool{}
	allOK := true
	hits, total := 0, 0
	// Stable representation order keeps output deterministic.
	repIDs := make([]string, 0, len(mf.Representations))
	for _, r := range mf.Representations {
		repIDs = append(repIDs, r.ID)
	}
	sort.Strings(repIDs)
	for _, repID := range repIDs {
		rep := findRep(mf, repID)
		tl, sr, ok := processOneRep(root, mf, rep, params, cr, builder)
		if !ok {
			allOK = false
			continue
		}
		chunksByRep[repID] = sr.Chunks
		// Keyframe alignment is a video property; audio tracks have no
		// random-access frames and must not dilute the hit-rate statistic.
		if rep.Kind == model.TrackVideo {
			hits += sr.KeyframeHits
			total += sr.BoundaryTotal
		}
		for _, c := range sr.Chunks {
			fp := core.HashChunk(c)
			hashSet[fp.Hash] = true
			builder.AddChunk(provenance.ChunkRecord{
				ChunkID: c.ID, Hash: fp.Hash, Representation: repID,
				Start: c.Start, End: c.End, KeyFramePTS: c.Boundary.KeyFramePTS,
				Sources: []provenance.SourceRef{{
					Manifest: mf.Path, Representation: repID,
					Entry: c.Boundary.SourceFrame, File: segmentFile(mf, rep, c),
				}},
			})
		}
		if rep.Kind == model.TrackAudio {
			report := core.DetectSilence(tl.Frames, model.TrackAudio,
				core.SilenceOptions{
					EnergyThreshold: params.SilenceThreshold,
					MinDuration:     params.SilenceMinDurNS,
				})
			cr.SilenceCount += len(report.Intervals)
			builder.AddSilence(provenance.SilenceRecord{
				Representation: repID, Incomplete: report.Incomplete,
				Reason: report.Reason,
			})
		}
	}
	if allOK {
		dd := core.DedupChunks(chunksByRep, blocked)
		cr.DeduplicatedCount = dd.DuplicateCount
		cr.SegmentHashes = sortedKeys(hashSet)
		if total == 0 {
			cr.KeyframeHitRate = 1.0
		} else {
			cr.KeyframeHitRate = float64(hits) / float64(total)
		}
	}
	return allOK
}

func processOneRep(root string, mf *model.Manifest, rep model.Representation,
	params Parameters, cr *CaseResult, builder *provenance.Builder,
) (*core.TimelineReport, *core.SplitResult, bool) {
	segs := make([]core.SegmentInput, 0, len(rep.Segments))
	for _, seg := range rep.Segments {
		path := resolveMediaPath(root, mf, rep, seg)
		track, err := adapter.Parse(adapter.MediaInput{
			Path: path, Representation: rep.ID, Entry: seg.Index, Manifest: mf.Path,
		})
		if err != nil {
			se := mustSE(err)
			cr.Errors = append(cr.Errors, journal.SourcedError{
				Kind: string(se.Kind), Msg: se.Msg, Loc: se.Error(),
			})
			builder.AddError(*se)
			return nil, nil, false
		}
		segs = append(segs, core.SegmentInput{
			Track: track, Anchor: seg.Start, Discontinuity: seg.Discontinuity,
		})
	}
	tl, err := core.BuildTimeline(rep.ID, rep.Kind, blockedFor(mf, rep.ID), segs)
	if err != nil {
		se := mustSE(err)
		cr.Errors = append(cr.Errors, journal.SourcedError{
			Kind: string(se.Kind), Msg: se.Msg, Loc: se.Error(),
		})
		builder.AddError(*se)
		return nil, nil, false
	}
	sr, err := core.Split(rep.ID, tl.Frames,
		core.SplitOptions{TargetDuration: params.TargetDurationNS})
	if err != nil {
		se := mustSE(err)
		cr.Errors = append(cr.Errors, journal.SourcedError{
			Kind: string(se.Kind), Msg: se.Msg, Loc: se.Error(),
		})
		return nil, nil, false
	}
	return tl, sr, true
}

func findRep(mf *model.Manifest, id string) model.Representation {
	for _, r := range mf.Representations {
		if r.ID == id {
			return r
		}
	}
	return model.Representation{}
}

func blockedFor(mf *model.Manifest, id string) bool {
	return core.MergeBlocked(mf, id)
}

func resolveMediaPath(root string, mf *model.Manifest, rep model.Representation, seg model.SegmentRef) string {
	if mf.Kind == "rtp" {
		// mf.Path is already relative to the fixture root.
		return filepath.Join(root, mf.Path)
	}
	if mf.Kind == "hls" {
		// HLS segment URLs were rebased to be relative to the fixture root.
		return filepath.Join(root, seg.URL)
	}
	// DASH: segment URLs are relative to the manifest's directory; mf.Path
	// itself is relative to the fixture root.
	manifestDir := filepath.Dir(mf.Path)
	return filepath.Join(root, manifestDir, seg.URL)
}

func segmentFile(mf *model.Manifest, rep model.Representation, c core.Chunk) string {
	for _, s := range rep.Segments {
		if s.Start <= c.Start && c.Start < s.Start+s.Duration {
			return s.URL
		}
	}
	if len(rep.Segments) > 0 {
		return rep.Segments[len(rep.Segments)-1].URL
	}
	return ""
}

func mustSE(err error) *model.SourceError {
	if se, ok := model.AsSource(err); ok {
		return se
	}
	return model.NewError(model.KindInternal,
		model.SourceLoc{Entry: -1, Frame: -1}, err.Error())
}

func toJournalErr(se *model.SourceError) journal.SourcedError {
	return journal.SourcedError{Kind: string(se.Kind), Msg: se.Msg, Loc: se.Error()}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// keep fixture import referenced for case constants used by the CLI layer.
var _ = fixture.CaseNormalDASH

func journalStatePath(stateDir, digest string) string {
	return filepath.Join(stateDir, digest)
}
