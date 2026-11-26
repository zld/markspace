package task

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"assetcheck/internal/analyze"
	"assetcheck/internal/cache"
	"assetcheck/internal/ebml"
	"assetcheck/internal/manifest"
)

type Input struct {
	SourcePath        string
	AssetID           string
	RequiredLanguages []string
	ExpectedCues      []manifest.ExpectedCue
	CrashAfter        Stage
}

type Result struct {
	Key              string
	ResumeFrom       Stage
	AlreadyDone      bool
	IndexRebuilt     bool
	CacheHit         bool
	CacheInvalidated bool
	Committed        bool
	Deduplicated     bool
	ManifestPath     string
	ManifestSHA256   string
	Manifest         *manifest.Manifest
	Report           *analyze.Report
}

type Runner struct {
	WorkDir     string
	store       *Store
	content     *cache.ContentCache
	indexPath   string
	manifestDir string
}

func NewRunner(workDir string) *Runner {
	return &Runner{
		WorkDir:     workDir,
		store:       NewStore(workDir),
		content:     cache.NewContentCache(filepath.Join(workDir, "cache", "content")),
		indexPath:   filepath.Join(workDir, "cache", "structure-index.json"),
		manifestDir: filepath.Join(workDir, "manifests"),
	}
}

func shouldCrash(in Input, st Stage) bool {
	return in.CrashAfter != "" && in.CrashAfter == st
}

// Process verifies the input fingerprint, holds the stable-key delivery lock,
// and resumes from the last atomically committed safe stage.
func (r *Runner) Process(in Input) (*Result, error) {
	contentSHA, err := cache.HashFile(in.SourcePath)
	if err != nil {
		return nil, err
	}
	identity, err := cache.IdentityForPath(in.SourcePath)
	if err != nil {
		return nil, err
	}
	key := BundleStableKey(in.AssetID, in.RequiredLanguages,
		[]string{"audio", "subtitle", "effect"}, contentSHA)

	lock, err := r.store.AcquireLock(key)
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	prior, err := r.store.Load(key)
	if err != nil {
		return nil, err
	}
	res := &Result{Key: key}
	var state *State
	if prior != nil && prior.InputSHA256 == contentSHA {
		state = prior
		res.ResumeFrom = prior.Stage
		res.AlreadyDone = prior.Stage == StageDone
		res.Deduplicated = res.AlreadyDone
	} else {
		state = &State{StableKey: key, AssetID: in.AssetID}
	}
	state.SourcePath = in.SourcePath
	state.InputSHA256 = contentSHA
	state.InputIdentity = identity

	if StageRank(state.Stage) < StageRank(StageDiscovered) {
		state.Stage = StageDiscovered
		if err := r.store.SaveState(state); err != nil {
			return nil, err
		}
		if shouldCrash(in, StageDiscovered) {
			return nil, ErrInterrupted
		}
	}

	doc, err := r.loadOrParseStructure(key, in.SourcePath, state)
	if err != nil {
		return nil, err
	}

	indexRebuilt, cacheInvalidated, err := r.reconcileIndex(in.SourcePath, identity, contentSHA, in.AssetID, doc)
	if err != nil {
		return nil, err
	}
	res.IndexRebuilt = indexRebuilt
	res.CacheInvalidated = cacheInvalidated

	if StageRank(state.Stage) < StageRank(StageParsed) {
		p, err := r.store.SaveStructure(key, doc)
		if err != nil {
			return nil, err
		}
		state.StructurePath = p
		state.Stage = StageParsed
		if err := r.store.SaveState(state); err != nil {
			return nil, err
		}
		if shouldCrash(in, StageParsed) {
			return nil, ErrInterrupted
		}
	}

	rep, cacheHit, err := r.loadOrMeasure(in.SourcePath, contentSHA, in.AssetID, state, doc)
	if err != nil {
		return nil, err
	}
	res.CacheHit = cacheHit
	res.Report = rep
	if StageRank(state.Stage) < StageRank(StageMeasured) {
		p, err := r.store.SaveReport(key, rep)
		if err != nil {
			return nil, err
		}
		state.ReportPath = p
		state.Stage = StageMeasured
		if err := r.store.SaveState(state); err != nil {
			return nil, err
		}
		if shouldCrash(in, StageMeasured) {
			return nil, ErrInterrupted
		}
	}

	var m *manifest.Manifest
	wroteManifest := false
	if StageRank(state.Stage) < StageRank(StageMerged) || state.ManifestSHA256 == "" {
		req := buildManifestRequest(in, contentSHA, rep)
		m, err = manifest.Merge(req)
		if err != nil {
			return nil, err
		}
		mp := filepath.Join(r.manifestDir, key+".json")
		sum, _, err := manifest.WriteAtomic(mp, m, 0o644)
		if err != nil {
			return nil, err
		}
		state.ManifestPath = mp
		state.ManifestSHA256 = sum
		state.Stage = StageMerged
		if err := r.store.SaveState(state); err != nil {
			return nil, err
		}
		wroteManifest = true
		if shouldCrash(in, StageMerged) {
			return nil, ErrInterrupted
		}
	} else {
		m, err = loadAndVerifyManifest(state.ManifestPath, state.ManifestSHA256)
		if err != nil {
			return nil, err
		}
	}
	res.Manifest = m
	res.ManifestPath = state.ManifestPath
	res.ManifestSHA256 = state.ManifestSHA256
	res.Committed = wroteManifest

	if StageRank(state.Stage) < StageRank(StageDone) {
		state.Stage = StageDone
		state.Committed = true
		if err := r.store.SaveState(state); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func (r *Runner) loadOrParseStructure(key, source string, state *State) (*ebml.Document, error) {
	if StageRank(state.Stage) >= StageRank(StageParsed) {
		var doc ebml.Document
		ok, err := r.store.LoadStructure(key, &doc)
		if err != nil {
			return nil, err
		}
		if ok {
			return &doc, nil
		}
	}
	return ebml.ParseFile(source, ebml.Options{})
}

func (r *Runner) reconcileIndex(source string, id cache.FileIdentity, contentSHA, assetID string, doc *ebml.Document) (bool, bool, error) {
	idx, err := cache.LoadIndex(r.indexPath)
	if err != nil {
		return false, false, err
	}
	if _, ok := idx.Lookup(source, id, doc.StructureFingerprint); ok {
		return false, false, nil
	}
	invalidated := false
	if old, ok := idx.Previous(source); ok && old.ContentSHA != contentSHA {
		if err := r.content.Invalidate(old.ContentSHA, analyze.Version); err != nil {
			return false, false, err
		}
		invalidated = true
	}
	idx.Put(cache.StructureRecord{
		Path:        source,
		Identity:    id,
		Fingerprint: doc.StructureFingerprint,
		ContentSHA:  contentSHA,
		AssetID:     assetID,
	})
	if err := idx.Save(r.indexPath); err != nil {
		return false, false, err
	}
	return true, invalidated, nil
}

func (r *Runner) loadOrMeasure(source, contentSHA, assetID string, state *State, doc *ebml.Document) (*analyze.Report, bool, error) {
	var rep analyze.Report
	cacheHit, err := r.content.Get(contentSHA, analyze.Version, &rep)
	if err != nil {
		return nil, false, err
	}
	if cacheHit {
		return &rep, true, nil
	}
	if StageRank(state.Stage) >= StageRank(StageMeasured) {
		ok, err := r.store.LoadReport(state.StableKey, &rep)
		if err != nil {
			return nil, false, err
		}
		if ok {
			_ = r.content.Put(contentSHA, analyze.Version, rep)
			return &rep, false, nil
		}
	}

	collector := analyze.NewCollector(assetID, doc)
	if _, err := ebml.ParseFile(source, ebml.Options{HandleEvent: collector.HandleEvent}); err != nil {
		return nil, false, err
	}
	full, err := collector.Report()
	if err != nil {
		return nil, false, err
	}
	if err := r.content.Put(contentSHA, analyze.Version, full); err != nil {
		return nil, false, err
	}
	return full, false, nil
}

func loadAndVerifyManifest(path, wantSHA string) (*manifest.Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if got := manifest.SHA256Bytes(b); got != wantSHA {
		return nil, errors.New("task manifest checksum mismatch")
	}
	var m manifest.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
