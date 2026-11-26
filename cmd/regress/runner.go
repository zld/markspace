package main

import (
	"os"
	"path/filepath"

	"cmafrepair/pkg/media/cache"
	"cmafrepair/pkg/media/checkpoint"
	"cmafrepair/pkg/media/clock"
	"cmafrepair/pkg/media/cmaf"
	"cmafrepair/pkg/media/fixture"
	"cmafrepair/pkg/media/index"
	"cmafrepair/pkg/media/manifest"
	"cmafrepair/pkg/media/pipeline"
)

// report aggregates everything a repair run prints.
type report struct {
	reps             int
	fragmentsTotal   int
	fragmentsResume  int
	repaired         int
	gap              int
	truncated        int
	indexEntries     int
	indexGap         int
	defects          []defectLine
	bundleSHA        string
	checksumMatch    bool
	interrupted      bool
	cacheHits        int
	cacheMisses      int
	rtpWraps         int
	rtpRollbacks     int
	boundaryMismatch int
	// localizeHit/localizeTotal drive the defect localization accuracy ratio.
	localizeHit   int
	localizeTotal int
}

// timelineCarry keeps each representation's repaired timeline for the join.
type timelineCarry struct {
	rep      string
	timeline *cmaf.Timeline
	anchor   clock.ClockAnchor
}

type defectLine struct {
	code   string
	rep    string
	frag   string
	box    string
	offset int64
	phase  string
}

type runner struct {
	in, out string
	reps    []string
	anchors fixture.AnchorFile
	cache   *cache.Store
	cp      *checkpoint.Store

	interruptAfter int
	resume         bool
	processed      int
	rep            report
	carries        []timelineCarry
}

// execute runs the repair across every representation and builds manifests
// and indexes. It returns an exitError carrying code 75 on injected
// interruption.
func (r *runner) execute() (*report, error) {
	variants := make([]manifest.Variant, 0, len(r.reps))
	for _, rep := range r.reps {
		pl, ix, err := r.processRep(rep)
		if err != nil {
			return &r.rep, err
		}
		variants = append(variants, manifest.Variant{
			Name: rep, Bandwidth: bandwidthFor(rep), Resolution: resolutionFor(rep),
			Codecs: codecsFor(rep), MediaPlaylist: pl,
		})
		r.rep.indexEntries += ix.DecodableCount()
		r.rep.indexGap += ix.GapCount()
		if err := r.writeIndex(rep, ix); err != nil {
			return &r.rep, err
		}
	}
	r.rep.reps = len(r.reps)

	// Fan repaired timelines into the shared clock and count mismatches.
	r.joinBoundaries()
	r.computeLocalization()

	// Master + per-rep media playlists.
	masterPath := filepath.Join(r.out, "master.m3u8")
	mf, err := os.Create(masterPath)
	if err != nil {
		return &r.rep, &exitError{code: exitUnsafe, msg: err.Error()}
	}
	if err := manifest.WriteMasterPlaylist(mf, variants); err != nil {
		mf.Close()
		return &r.rep, &exitError{code: exitUnsafe, msg: err.Error()}
	}
	mf.Close()
	for _, v := range variants {
		p := filepath.Join(r.out, v.Name, v.Name+".m3u8")
		var sb stringBuilderFile
		if err := writeText(p, sb.build(v.MediaPlaylist)); err != nil {
			return &r.rep, &exitError{code: exitUnsafe, msg: err.Error()}
		}
	}

	// Finalize bundle digest and a self-consistency checksum gate.
	r.rep.bundleSHA = r.cp.BundleHash()
	r.rep.checksumMatch = r.verifyAllCommitted()
	st := r.cache.Stats()
	r.rep.cacheHits, r.rep.cacheMisses = st.Hits, st.Misses
	return &r.rep, nil
}

// processRep repairs one representation end to end.
func (r *runner) processRep(rep string) (manifest.MediaPlaylist, *index.Index, error) {
	repIn := filepath.Join(r.in, rep)
	repOut := filepath.Join(r.out, rep)
	if err := os.MkdirAll(repOut, 0o755); err != nil {
		return manifest.MediaPlaylist{}, nil, &exitError{code: exitUnsafe, msg: err.Error()}
	}
	// Copy init segment through the checkpoint-aware path.
	if err := r.ensureInit(rep, repIn, repOut); err != nil {
		return manifest.MediaPlaylist{}, nil, err
	}
	init, err := cmaf.ParseInit(filepath.Join(repIn, "init.mp4"))
	if err != nil {
		return manifest.MediaPlaylist{}, nil, &exitError{code: exitInvalidInput, msg: err.Error()}
	}
	timeline := cmaf.NewTimelineRepair(rep, init)
	builder := index.NewBuilder(rep, init.Timescale)
	names, err := listFragments(repIn)
	if err != nil {
		return manifest.MediaPlaylist{}, nil, &exitError{code: exitInvalidInput, msg: err.Error()}
	}
	r.rep.fragmentsTotal += len(names)

	for _, name := range names {
		src := filepath.Join(repIn, name)
		f, perr := cmaf.ParseFragment(src)
		if perr != nil {
			return manifest.MediaPlaylist{}, nil, &exitError{code: exitInvalidInput, msg: perr.Error()}
		}
		entry, aerr := timeline.Add(f)
		if aerr != nil {
			return manifest.MediaPlaylist{}, nil, &exitError{code: exitUnsafe, msg: aerr.Error()}
		}
		r.recordDefects(rep, entry.Defects)
		if err := r.handleFragment(rep, repOut, src, f, entry, builder); err != nil {
			return manifest.MediaPlaylist{}, nil, err
		}
		// Interrupt injection happens after the fragment is handled.
		r.processed++
		if r.interruptAfter > 0 && r.processed >= r.interruptAfter {
			r.rep.interrupted = true
			return manifest.MediaPlaylist{}, nil,
				&exitError{code: exitInterrupted, msg: "injected interrupt after fragment " + itoa(r.processed)}
		}
	}
	tl := timeline.Build()
	r.rep.repaired += tl.RepairedCount
	r.rep.gap += tl.GapCount
	r.rep.truncated += tl.TruncatedCount
	anchor := r.rtpStats(rep, init.Timescale)
	r.carries = append(r.carries, timelineCarry{rep: rep, timeline: tl, anchor: anchor})
	pl := manifest.FromTimeline(tl)
	ix := builder.Build()
	return pl, ix, nil
}

// rtpStats runs the representation's publish timestamps through the RTP
// unwrapper and accumulates wrap/rollback counts, returning the anchor.
func (r *runner) rtpStats(rep string, hz uint32) clock.ClockAnchor {
	rec, ok := r.anchors.Reps[rep]
	anchor := clock.ClockAnchor{ClockHz: hz}
	if !ok {
		return anchor
	}
	if rec.ClockHz != 0 {
		anchor.ClockHz = rec.ClockHz
	}
	anchor.AnchorRTP = rec.AnchorRTP
	anchor.AnchorSharedMs = rec.AnchorSharedMs
	anchor.MediaToRTP = rec.MediaToRTP
	u := clock.NewUnwrapper(anchor.ClockHz)
	for _, ts := range rec.Timestamps {
		if _, ev, err := u.Observe(ts); err != nil {
			break
		} else if ev.Wrapped {
			r.rep.rtpWraps++
		} else if ev.Rollback {
			r.rep.rtpRollbacks++
		}
	}
	return anchor
}

// joinBoundaries fans the repaired timelines into the shared clock and
// records cross-rendition boundary mismatches.
func (r *runner) joinBoundaries() {
	if len(r.carries) < 2 {
		return
	}
	results := make([]pipeline.RepResult, 0, len(r.carries))
	for _, c := range r.carries {
		results = append(results, pipeline.RepResult{
			Name: c.rep, Timeline: c.timeline, Anchor: c.anchor,
		})
	}
	jr, err := pipeline.Join(results)
	if err != nil {
		return // no healthy rep; mismatch counting is left at zero
	}
	// Only numeric one-tick divergences count as boundary mismatches;
	// expected missing-sequence rows from injected gaps are excluded.
	n := 0
	for _, m := range jr.Mismatches {
		if m.DeltaMs != 0 || m.ReferenceMs != 0 {
			n++
		}
	}
	r.rep.boundaryMismatch = n
}

// computeLocalization derives the defect localization accuracy over distinct
// defect families. A family counts as located when its record names a box and
// carries a concrete byte offset, so one physical fault contributes once.
func (r *runner) computeLocalization() {
	seen := map[string]bool{}
	for _, d := range r.rep.defects {
		if seen[d.code] {
			continue
		}
		seen[d.code] = true
		r.rep.localizeTotal++
		if d.box != "" && d.offset >= 0 {
			r.rep.localizeHit++
		}
	}
}
