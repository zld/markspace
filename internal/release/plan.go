// Package release builds platform package plans from the timeline,
// renders only the assets whose inputs changed, and keeps recoverable
// version history for rollback.
package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"releasepipe/internal/errs"
	"releasepipe/internal/index"
	"releasepipe/internal/ingest"
)

// Asset is one platform-targeted output derived from one track.
type Asset struct {
	ID       string // "<kind>:<platform>"
	Kind     string
	Platform string
	Digest   string // covers source bytes, track params, muxing, platform
}

// BuildAssets computes the deterministic asset list for timeline ×
// platforms. srcDigests maps source path to content digest; containerMeta
// maps source path to muxing metadata, so a pure rewrap still invalidates.
func BuildAssets(tl *ingest.Timeline, platforms []string,
	srcDigests map[string]string, containerMeta map[string]map[string]string) []Asset {
	var assets []Asset
	for _, t := range tl.Tracks {
		srcDigest := srcDigests[t.SourcePath]
		metaDigest := index.HashMeta(containerMeta[t.SourcePath])
		trackDigest := index.HashMeta(t.Params)
		for _, p := range platforms {
			assets = append(assets, Asset{
				ID:       t.Kind + ":" + p,
				Kind:     t.Kind,
				Platform: p,
				Digest:   index.HashStrings(srcDigest, trackDigest, metaDigest, p, t.Kind),
			})
		}
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].ID < assets[j].ID })
	return assets
}

// Result summarizes one plan execution.
type Result struct {
	Records        []index.AssetRecord // final manifest order
	Reused         int                 // store-reused or checkpoint-adopted
	Rebuilt        int
	Durations      []time.Duration // per rebuilt asset, for p95
	ManifestSHA    string
	Interrupted    bool
	ResumedFrom    int // block index processing resumed from (0 = fresh)
	NextBlock      int // resume point after interruption
}

// Manifest is the published package manifest.
type Manifest struct {
	Platforms []string            `json:"platforms"`
	Inputs    map[string]string   `json:"inputs"` // source path -> digest
	Assets    []index.AssetRecord `json:"assets"`
}

// Process renders assets in fixed-size blocks with checkpoints. Without
// resume, committed store records short-circuit unchanged assets; with
// resume, completed blocks are adopted after digest verification.
func Process(dir string, assets []Asset, platforms []string, srcDigests map[string]string,
	store *index.Store, stopAfterBlock int, resume bool) (*Result, error) {
	var blocks [][]Asset
	for i := 0; i < len(assets); i += index.BlockSize {
		end := i + index.BlockSize
		if end > len(assets) {
			end = len(assets)
		}
		blocks = append(blocks, assets[i:end])
	}
	res := &Result{}
	startBlock := 0
	if resume {
		recs, next, err := index.LoadCheckpoint(dir)
		if err != nil {
			return nil, err
		}
		// Adopt completed blocks only while records match the plan;
		// the first mismatch becomes the resume point.
		adopted := 0
		for b := 0; b < next && b < len(blocks); b++ {
			match := true
			for j, a := range blocks[b] {
				if adopted+j >= len(recs) || recs[adopted+j].ID != a.ID ||
					recs[adopted+j].Digest != a.Digest {
					match = false
					break
				}
			}
			if !match {
				break
			}
			adopted += len(blocks[b])
			startBlock = b + 1
		}
		res.Records = append(res.Records, recs[:adopted]...)
		res.Reused += adopted
		res.ResumedFrom = startBlock
	}

	for b := startBlock; b < len(blocks); b++ {
		var recs []index.AssetRecord
		for _, a := range blocks[b] {
			if rec, ok := store.Lookup(a.ID, a.Digest); ok {
				res.Reused++
				recs = append(recs, rec)
				continue
			}
			t0 := time.Now()
			out, err := render(dir, a)
			if err != nil {
				return nil, err
			}
			res.Durations = append(res.Durations, time.Since(t0))
			res.Rebuilt++
			recs = append(recs, index.AssetRecord{
				ID: a.ID, Platform: a.Platform, Digest: a.Digest, Output: out,
			})
		}
		if err := index.AppendBlock(dir, b, recs); err != nil {
			return nil, err
		}
		res.Records = append(res.Records, recs...)
		if stopAfterBlock > 0 && b+1 >= stopAfterBlock && b+1 < len(blocks) {
			res.Interrupted = true
			res.NextBlock = b + 1
			return res, nil
		}
	}

	// All blocks complete: stage everything and commit the index once.
	for _, rec := range res.Records {
		store.Stage(rec)
	}
	if err := store.Commit(); err != nil {
		return nil, err
	}
	manifest := Manifest{Platforms: platforms, Inputs: srcDigests, Assets: res.Records}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, errs.Wrap(errs.StageRelease, errs.CodeInternal, "", -1, err)
	}
	res.ManifestSHA = index.HashBytes(raw)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
		return nil, errs.Wrap(errs.StageRelease, errs.CodeInternal, dir, -1, err)
	}
	if _, err := CommitVersion(dir, raw, res.ManifestSHA); err != nil {
		return nil, err
	}
	return res, nil
}

// render deterministically "encodes" one asset: the output bytes derive
// only from the asset digest, so identical inputs reproduce identical
// outputs and repeated runs never diverge.
func render(dir string, a Asset) (string, error) {
	rel := filepath.Join("assets", a.Platform, a.Kind+".bin")
	out := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", errs.Wrap(errs.StageRelease, errs.CodeInternal, a.ID, -1, err)
	}
	body := "releasepipe-asset\nid=" + a.ID + "\ndigest=" + a.Digest +
		"\npayload=" + index.HashStrings("render", a.Digest) + "\n"
	if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
		return "", errs.Wrap(errs.StageRelease, errs.CodeInternal, a.ID, -1, err)
	}
	return rel, nil
}
