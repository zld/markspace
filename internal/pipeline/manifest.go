package pipeline

import (
	"mediad/internal/checkpoint"
	errs "mediad/internal/errors"
	"mediad/internal/provenance"
)

// publishManifest records every stage, index evidence, damaged offset, skip
// reason and per-segment hash, then publishes atomically and binds the
// resulting hash into the final checkpoint.
func publishManifest(res *Result, opts Options,
	outcomes []SegmentOutcome, cp *checkpoint.State, path string,
	store *checkpoint.Store) error {
	m := provenance.New(opts.FixtureID, string(opts.Codec), res.InputHash, res.IndexHash)
	m.SetStage(provenance.Stage{Name: "scan", Status: provenance.StageSucceeded})
	m.SetStage(provenance.Stage{Name: "index", Status: provenance.StageSucceeded})
	m.SetStage(provenance.Stage{Name: "cut", Status: provenance.StageSucceeded})
	if res.CorruptSegmentCount > 0 {
		m.SetStage(provenance.Stage{Name: "analyze", Status: provenance.StagePartial,
			Detail: "corrupt segments retained with markers"})
	} else {
		m.SetStage(provenance.Stage{Name: "analyze", Status: provenance.StageSucceeded})
	}
	if res.Match != nil {
		m.IndexHits, m.IndexMisses, m.HitRate = res.Match.Hits, res.Match.Misses, res.Match.HitRate
		for _, mt := range res.Match.Matches {
			if !mt.Valid || (!mt.Hit && mt.Valid) {
				m.SkipReasons = append(m.SkipReasons, mt.Reason)
			}
		}
	}
	for _, d := range res.Damages {
		m.DamagedOffsets = append(m.DamagedOffsets, d.Offset)
	}
	firstOf := map[string]uint64{}
	for _, o := range outcomes {
		rec := provenance.SegmentRecord{
			ID: o.ID, SHA256: o.Hash, Corrupt: o.Corrupt,
			Reasons: append([]string(nil), o.Reasons...),
		}
		if origin, dup := firstOf[o.Hash]; dup {
			rec.Duplicate, rec.FirstOfHash = true, origin
		} else {
			firstOf[o.Hash] = o.ID
			rec.FirstOfHash = o.ID
		}
		m.Segments = append(m.Segments, rec)
	}
	hash, err := m.Publish(path) // atomic rename
	if err != nil {
		return err
	}
	cp.ManifestHash = hash
	cp.SegmentCount = len(outcomes)
	if err := store.Save(cp); err != nil {
		return errs.New(errs.Fatal, errs.CodeIOFailure, "final checkpoint: %v", err)
	}
	res.ManifestCanonicalHash, err = m.CanonicalHash()
	return err
}
