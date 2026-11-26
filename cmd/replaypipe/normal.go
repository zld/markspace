package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"replaypipe/internal/clip"
	"replaypipe/internal/fixtures"
	"replaypipe/internal/index"
	"replaypipe/internal/manifest"
	"replaypipe/internal/metadata"
	"replaypipe/internal/store"
)

// eventSet bundles the parsed event sources for the normal presentation.
type eventSet struct {
	SRT      []metadata.Event
	VTT      []metadata.Event
	Chapters []metadata.Event
	ID3      []metadata.Event
	SEI      []metadata.Event
	RTP      []metadata.Event
}

// loadEvents parses every side-band source against the media duration and
// unifies them into one event list.
func loadEvents(r *reporter, set *fixtures.Set, media *index.Media) (*eventSet, bool) {
	mediaEnd := media.DurationNS()
	opts := metadata.ParseOptions{MediaEndNS: mediaEnd}
	es := &eventSet{}
	ok := true

	if events, e := parseSRTFile(set.SRT, opts); e != nil {
		r.failf("parse srt: %v", e)
		ok = false
	} else {
		es.SRT = events
	}
	if events, e := parseVTTFile(set.VTT, opts); e != nil {
		r.failf("parse webvtt: %v", e)
		ok = false
	} else {
		es.VTT = events
	}
	if events, e := parseChapterFile(set.Chapters, opts); e != nil {
		r.failf("parse chapters: %v", e)
		ok = false
	} else {
		es.Chapters = events
	}
	if data, err := os.ReadFile(set.ID3); err == nil {
		if events, e := metadata.ParseID3(data, metadata.ParseOptions{MediaEndNS: mediaEnd, TrackID: 3}); e != nil {
			r.failf("parse id3: %v", e)
			ok = false
		} else {
			es.ID3 = events
		}
	}
	if data, err := os.ReadFile(set.SEI); err == nil {
		if events, e := metadata.ParseSEI(data, metadata.ParseOptions{MediaEndNS: mediaEnd, TrackID: 1}); e != nil {
			r.failf("parse sei: %v", e)
			ok = false
		} else {
			es.SEI = events
		}
	}
	if data, err := os.ReadFile(set.RTP); err == nil {
		if sess, e := metadata.ParseRTP(data, fixtures.Timescale, metadata.ParseOptions{MediaEndNS: mediaEnd, TrackID: 1}); e != nil {
			r.failf("parse rtp: %v", e)
			ok = false
		} else {
			es.RTP = sess.Events
			r.printf("rtp: %s", sess.RTPSummary())
		}
	}
	return es, ok
}

func parseSRTFile(path string, opts metadata.ParseOptions) ([]metadata.Event, *errorWrap) {
	f, err := os.Open(path)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer f.Close()
	events, e := metadata.ParseSRT(f, opts)
	if e != nil {
		return nil, wrapErr(e)
	}
	return events, nil
}

func parseVTTFile(path string, opts metadata.ParseOptions) ([]metadata.Event, *errorWrap) {
	f, err := os.Open(path)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer f.Close()
	events, e := metadata.ParseWebVTT(f, opts)
	if e != nil {
		return nil, wrapErr(e)
	}
	return events, nil
}

func parseChapterFile(path string, opts metadata.ParseOptions) ([]metadata.Event, *errorWrap) {
	f, err := os.Open(path)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer f.Close()
	events, e := metadata.ParseChapters(f, opts)
	if e != nil {
		return nil, wrapErr(e)
	}
	return events, nil
}

// errorWrap makes helper signatures uniform without importing timebase twice.
type errorWrap struct{ err error }

func (e *errorWrap) Error() string { return e.err.Error() }

func wrapErr(err error) *errorWrap { return &errorWrap{err: err} }

func (e *errorWrap) Err() error { return e.err }

// runNormal performs the primary acceptance flow: parse media and events,
// build a plan, write every slice, commit the manifest, then re-run to prove
// deterministic hashes and zero recomputation.
func runNormal(r *reporter, cfg *config, set *fixtures.Set) bool {
	r.section("normal: media and events")
	media, err := index.ParseFile(set.Normal, index.DefaultLimits())
	if err != nil {
		r.failf("parse media: %v", err)
		return false
	}
	r.printf("media: tracks=%d video=%dx%d@%.3f fps fragmented=%t duration=%dns",
		len(media.Tracks), media.VideoTrack().Width, media.VideoTrack().Height,
		media.VideoTrack().FrameRate(), media.Fragmented, media.DurationNS())
	r.printf("media content digest: %s", media.ContentDigest)
	r.printf("media envelope digest: %s", media.EnvelopeDigest)

	es, ok := loadEvents(r, set, media)
	if !ok {
		return false
	}
	// Round-trip consistency is measured by re-parsing each source and
	// comparing canonical digests.
	consistent, checked := roundTripRate(set, es, media)
	r.printf("round-trip parse consistency: %d/%d", consistent, checked)
	r.check("round-trip consistency is 100%", consistent == checked && checked > 0,
		fmt.Sprintf("%d/%d", consistent, checked))

	events := append(append(append(append([]metadata.Event{}, es.SRT...), es.Chapters...), es.ID3...), es.SEI...)
	events = append(events, es.RTP...)
	media.SetEvents(events)
	r.printf("events: srt=%d vtt=%d chapters=%d id3=%d sei=%d rtp=%d event-digest=%s",
		len(es.SRT), len(es.VTT), len(es.Chapters), len(es.ID3), len(es.SEI), len(es.RTP), media.EventDigest)

	kf, e := index.BuildKeyframeIndex(media.VideoTrack())
	if e != nil {
		r.failf("keyframe index: %v", e)
		return false
	}
	r.printf("keyframes: %s", kf.String())

	// Events used for cutting come from SRT (three cues at keyframe times).
	cutEvents := es.SRT
	// A published manifest from an earlier run supplies the asset digests so
	// unchanged slices are reused instead of re-cut.
	priorManifest, priorErr := loadPublished(cfg.root)
	var prior map[string]string
	if priorErr == nil {
		prior = map[string]string{}
		for _, entry := range priorManifest.Entries {
			prior[entry.SliceID] = entry.AssetDigest
		}
	}
	plan, e := clip.BuildPlan(clip.PlanOptions{
		Media: media, Keyframes: kf, Events: cutEvents, Budget: cfg.budget(), Known: prior,
	})
	if e != nil {
		r.failf("plan: %v", e)
		return false
	}
	r.printf("plan: slices=%d reused=%d planned=%d", len(plan.Slices), plan.ReusedCount, plan.PlannedCount)
	hits := keyframeHits(kf, plan)
	r.check("keyframe hits 8/8", hits == 8, fmt.Sprintf("%d/8", hits))

	clipDir := filepath.Join(cfg.root, "clips")
	written, ok := writeChanged(r, cfg, media, plan, clipDir)
	if !ok {
		return false
	}
	if len(written) == 0 && plan.ReusedCount > 0 {
		r.printf("every slice reused; no sample bytes were copied")
	}
	mfst := manifestFromPlan(plan, written, media, priorManifest)
	data, e := manifest.Encode(mfst)
	if e != nil {
		r.failf("encode manifest: %v", e)
		return false
	}
	var assets []store.File
	for _, w := range written {
		raw, err := os.ReadFile(w.Path)
		if err != nil {
			r.failf("read slice %s: %v", w.SliceID, err)
			return false
		}
		assets = append(assets, store.File{Name: filepath.Join("clips", filepath.Base(w.Path)), Data: raw, Sum: w.SHA256})
	}
	res, e := store.Commit(cfg.root, "manifest.json", data, assets, store.CommitOptions{})
	if e != nil {
		r.failf("commit: %v", e)
		return false
	}
	r.printf("manifest: sha256=%s committed=%t assets=%d", res.ManifestSHA, res.Committed, len(res.Files))
	r.printf("slice count: %d", len(written))
	hashesOK := reportHashes(r, written)
	r.check("slice hashes reported", hashesOK, "")
	r.check("slice count is 3", len(plan.Slices) == 3, fmt.Sprintf("%d", len(plan.Slices)))
	firstRun := plan.PlannedCount > 0

	// Second run reads the published manifest back from disk: the asset
	// digests recorded there must make every slice reusable and the on-disk
	// hashes must be unchanged.
	known := map[string]string{}
	if pub, err := loadPublished(cfg.root); err == nil {
		for _, entry := range pub.Entries {
			known[entry.SliceID] = entry.AssetDigest
		}
	}
	plan2, e := clip.BuildPlan(clip.PlanOptions{
		Media: media, Keyframes: kf, Events: cutEvents, Budget: cfg.budget(),
		Known: known,
	})
	if e != nil {
		r.failf("plan rerun: %v", e)
		return false
	}
	recomputed := plan2.PlannedCount
	r.check("rerun recomputation is 0", recomputed == 0, fmt.Sprintf("recomputed=%d", recomputed))
	if firstRun {
		r.check("first run planned all slices", plan.ReusedCount == 0,
			fmt.Sprintf("reused=%d", plan.ReusedCount))
	}
	r.check("rerun reuses every slice", plan2.ReusedCount == len(plan2.Slices),
		fmt.Sprintf("reused=%d/%d", plan2.ReusedCount, len(plan2.Slices)))
	same, detail := compareDiskHashes(cfg.root)
	r.check("rerun hashes identical", same, detail)
	return true
}

// writeChanged writes only slices whose asset digest is new. Reused slices keep
// the assets already on disk untouched.
func writeChanged(r *reporter, cfg *config, media *index.Media, plan *clip.Plan, dir string) ([]*clip.Written, bool) {
	w, e := clip.NewWriter(media, dir)
	if e != nil {
		r.failf("open writer: %v", e)
		return nil, false
	}
	w.MaxOutBytes = cfg.maxOutBytes
	defer w.Close()
	var out []*clip.Written
	for _, s := range plan.Slices {
		if s.Reused {
			continue
		}
		wr, e := w.WriteSlice(s)
		if e != nil {
			r.failf("write %s: %v", s.ID, e)
			return nil, false
		}
		out = append(out, wr)
	}
	return out, true
}

func reportHashes(r *reporter, written []*clip.Written) bool {
	ok := true
	for _, w := range written {
		r.printf("  %s event=%s start=%dns aligned=%dns size=%d sha256=%s",
			w.SliceID, w.EventID, w.EventStartNS, w.AlignedStartNS, w.Size, w.SHA256)
		if w.SHA256 == "" {
			ok = false
		}
	}
	return ok
}

// loadPublished reads the committed manifest, if any.
func loadPublished(root string) (*manifest.Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return nil, err
	}
	m, e := manifest.Adapter{}.Read(data)
	if e != nil {
		return nil, e
	}
	return m, nil
}

// compareDiskHashes re-hashes every asset referenced by the published manifest
// and reports whether the recorded and actual digests still agree.
func compareDiskHashes(root string) (bool, string) {
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return false, err.Error()
	}
	m, e := manifest.Adapter{}.Read(data)
	if e != nil {
		return false, e.Error()
	}
	for _, entry := range m.Entries {
		raw, err := os.ReadFile(filepath.Join(root, "clips", filepath.Base(entry.Path)))
		if err != nil {
			return false, entry.SliceID + ": " + err.Error()
		}
		if got := sha256HexCLI(raw); got != entry.SHA256 {
			return false, entry.SliceID + " hash drifted"
		}
	}
	return true, fmt.Sprintf("%d assets", len(m.Entries))
}

// keyframeHits verifies that every indexed keyframe resolves exactly and that
// every slice starts on one of them. The count is the number of keyframes
// proven usable as cut points.
func keyframeHits(kf *index.KeyframeIndex, plan *clip.Plan) int {
	times := make([]int64, 0, kf.Len())
	for i := 0; i < kf.Len(); i++ {
		times = append(times, kf.Frame(i).PTSNS())
	}
	resolved, stats := kf.Resolve(times)
	for _, s := range plan.Slices {
		if _, ok := resolved[s.AlignedStartNS]; !ok {
			return 0
		}
	}
	return stats.Exact
}

func manifestFromPlan(plan *clip.Plan, written []*clip.Written, media *index.Media, prior *manifest.Manifest) *manifest.Manifest {
	m := &manifest.Manifest{
		Version:             manifest.Version,
		MediaContentDigest:  plan.MediaContentDigest,
		MediaEnvelopeDigest: plan.MediaEnvelopeDigest,
		EventDigest:         plan.EventDigest,
	}
	priorEntry := map[string]manifest.Entry{}
	if prior != nil {
		for _, entry := range prior.Entries {
			priorEntry[entry.SliceID] = entry
		}
	}
	writtenByID := map[string]*clip.Written{}
	for _, w := range written {
		writtenByID[w.SliceID] = w
	}
	for _, s := range plan.Slices {
		w := writtenByID[s.ID]
		entry := manifest.Entry{
			SliceID: s.ID, EventID: s.EventID, EventKind: string(s.EventKind),
			EventStartNS: s.EventStartNS, EventEndNS: s.EventEndNS,
			AlignedStartNS: s.AlignedStartNS, AlignedEndNS: s.AlignedEndNS,
			Path: s.ID + ".mp4", AssetDigest: s.AssetDigest(media), Quality: manifest.Quality{
				Width: s.Quality.Width, Height: s.Quality.Height, FPS: s.Quality.FPS,
				GateOK: s.Quality.GateOK, BudgetOK: s.Quality.BudgetOK,
				Representation: s.Quality.Representation,
			},
		}
		if w != nil {
			entry.Path = filepath.Base(w.Path)
			entry.Size = w.Size
			entry.SHA256 = w.SHA256
			entry.Quality.Width = w.Quality.Width
			entry.Quality.Height = w.Quality.Height
			entry.Quality.FPS = w.Quality.FPS
		} else if old, ok := priorEntry[s.ID]; ok && s.Reused {
			// Reused slices keep the published file and its recorded hash.
			entry.Path = old.Path
			entry.Size = old.Size
			entry.SHA256 = old.SHA256
			entry.Quality = old.Quality
		}
		m.Entries = append(m.Entries, entry)
	}
	m.Digest = m.ComputeDigest()
	return m
}

func sha256HexCLI(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var _ = time.Second
