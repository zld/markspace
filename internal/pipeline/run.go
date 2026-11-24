// Package pipeline wires manifest parsing, media adapters, the core
// processors and the journal/provenance stores into one deterministic,
// resumable run over a generated fixture tree.
package pipeline

import (
	"path/filepath"
	"strconv"

	"mediad/internal/adapter"
	_ "mediad/internal/adapter/mp4"
	_ "mediad/internal/adapter/rtp"
	"mediad/internal/fixture"
	"mediad/internal/journal"
	"mediad/internal/manifest"
	"mediad/internal/model"
	"mediad/internal/provenance"
)

// Parameters are the tunable processing options; they enter the config
// digest so changing any of them starts a fresh state line.
type Parameters struct {
	TargetDurationNS  model.PlayTime `json:"target_duration_ns"`
	SilenceThreshold  float64        `json:"silence_energy_threshold"`
	SilenceMinDurNS   model.PlayTime `json:"silence_min_duration_ns"`
}

// DefaultParameters are the values used by the CLI unless overridden.
func DefaultParameters() Parameters {
	return Parameters{
		// Fixtures place a keyframe every second; a 1 s target makes every
		// internal boundary land exactly on a keyframe for the happy path.
		TargetDurationNS: 1_000_000_000,
		SilenceThreshold: 5,
		SilenceMinDurNS:  0,
	}
}

func (p Parameters) asMap() map[string]string {
	return map[string]string{
		"target_duration_ns":      strconv.FormatInt(int64(p.TargetDurationNS), 10),
		"silence_energy_threshold": strconv.FormatFloat(p.SilenceThreshold, 'f', -1, 64),
		"silence_min_duration_ns": strconv.FormatInt(int64(p.SilenceMinDurNS), 10),
	}
}

// CaseResult is the per-case block of the JSON report.
type CaseResult struct {
	ID                   string   `json:"id"`
	OK                   bool     `json:"ok"`
	RoundtripConsistency float64  `json:"roundtrip_consistency"`
	KeyframeHitRate      float64  `json:"keyframe_hit_rate"`
	SegmentHashes        []string `json:"segment_hashes"`
	SilenceCount         int      `json:"silence_count"`
	DeduplicatedCount    int      `json:"deduplicated_count"`
	Errors               []journal.SourcedError `json:"errors"`
	Resumed              bool     `json:"resumed"`
}

// Report is the top-level JSON document.
type Report struct {
	Cases            []CaseResult `json:"cases"`
	RoundtripConsistency float64  `json:"roundtrip_consistency"`
	KeyframeHitRate  float64      `json:"keyframe_hit_rate"`
	SegmentHashes    []string     `json:"segment_hashes"`
	SilenceCount     int          `json:"silence_count"`
	DeduplicatedCount int         `json:"deduplicated_count"`
	Errors           []journal.SourcedError `json:"errors"`
	ResumedCount     int          `json:"resumed_count"`
}

// Run executes every case in the fixture index, resuming committed items.
func Run(fixtureRoot, stateDir string, params Parameters) (*Report, error) {
	return RunFiltered(fixtureRoot, stateDir, params, nil)
}

// RunFiltered behaves like Run but processes only the cases listed in allow.
// A nil allow list means every case in the index.
func RunFiltered(fixtureRoot, stateDir string, params Parameters, allow []string) (*Report, error) {
	indexPath := filepath.Join(fixtureRoot, "cases.json")
	indexBytes, err := readFile(indexPath)
	if err != nil {
		return nil, model.NewError(model.KindManifestMissing,
			model.SourceLoc{Manifest: indexPath, Entry: -1, Frame: -1}, err.Error())
	}
	var index fixture.Index
	if err := unmarshal(indexBytes, &index); err != nil {
		return nil, model.WrapError(model.KindCorruptBox,
			model.SourceLoc{Manifest: indexPath, Entry: -1, Frame: -1}, err)
	}
	allowed := map[string]bool{}
	for _, id := range allow {
		allowed[id] = true
	}
	rep := &Report{}
	aggHashes := map[string]bool{}
	var rtSum, kfSum float64
	rtN, kfN := 0, 0
	for _, c := range index.Cases {
		if len(allowed) > 0 && !allowed[c.ID] {
			continue
		}
		cr := runCase(fixtureRoot, stateDir, c, params)
		rep.Cases = append(rep.Cases, cr)
		if cr.OK {
			rtSum += cr.RoundtripConsistency
			kfSum += cr.KeyframeHitRate
			rtN++
			kfN++
			for _, h := range cr.SegmentHashes {
				aggHashes[h] = true
			}
			rep.SilenceCount += cr.SilenceCount
			rep.DeduplicatedCount += cr.DeduplicatedCount
		}
		if cr.Resumed {
			rep.ResumedCount++
		}
		rep.Errors = append(rep.Errors, cr.Errors...)
	}
	if rtN > 0 {
		rep.RoundtripConsistency = rtSum / float64(rtN)
	}
	if kfN > 0 {
		rep.KeyframeHitRate = kfSum / float64(kfN)
	}
	rep.SegmentHashes = sortedKeys(aggHashes)
	return rep, nil
}

func runCase(fixtureRoot, stateDir string, c fixture.Case, params Parameters) CaseResult {
	cr := CaseResult{ID: c.ID, RoundtripConsistency: 0, KeyframeHitRate: 0}
	// Manifest paths inside cases.json are relative to the fixture root and
	// stay relative; only the digest calculation reads the absolute file.
	relManifest := c.Manifest
	manifestPath := filepath.Join(fixtureRoot, relManifest)
	manifestSHA, _ := journal.SHA256File(manifestPath)
	digestCfg := journal.ConfigDigest{
		ManifestSHA:    manifestSHA,
		ParserVersions: adapter.RegisteredVersions(),
		Parameters:     params.asMap(),
	}
	digest, _ := digestCfg.Digest()
	j := journal.Open(stateDir, digest)
	if committed, _ := j.IsCommitted(c.ID); committed {
		// Resume: reuse the committed result verbatim, hashes included.
		_, item, _ := j.Status(c.ID)
		cr.Resumed = true
		cr.OK = true
		if item != nil && len(item.Result) > 0 {
			_ = unmarshal(item.Result, &cr)
			cr.Resumed = true
		}
		return cr
	}
	builder := provenance.NewBuilder(c.ID, adapter.RegisteredVersions(), params.asMap())
	mf, perr := parseManifest(c.Kind, manifestPath, relManifest)
	if perr != nil {
		cr.Errors = append(cr.Errors, toJournalErr(perr))
		builder.AddError(*perr)
		_ = j.RecordFailure(c.ID, cr.Errors, 1)
		_, _ = builder.Write(journalStatePath(stateDir, digest))
		return cr
	}
	cr.RoundtripConsistency, _, _ = manifest.RoundtripConsistency(mf)
	processOK := processRepresentations(fixtureRoot, mf, params, &cr, builder)
	cr.OK = processOK
	if processOK {
		raw, _ := marshalResult(cr)
		if err := j.Commit(journal.Item{CaseID: c.ID, Result: raw}); err != nil {
			cr.OK = false
			cr.Errors = append(cr.Errors, journal.SourcedError{
				Kind: string(model.KindInternal), Msg: err.Error(),
			})
		}
	} else {
		_ = j.RecordFailure(c.ID, cr.Errors, 1)
	}
	_, _ = builder.Write(journalStatePath(stateDir, digest))
	return cr
}
