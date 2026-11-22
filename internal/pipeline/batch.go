package pipeline

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/pairwise/mediad/internal/checkpoint"
	"github.com/pairwise/mediad/internal/fault"
	"github.com/pairwise/mediad/internal/fingerprint"
)

// SamplesPerBlock is the number of media samples committed per checkpoint
// block. The journal is fsynced and the checkpoint published atomically at
// this boundary, so an interruption replays at most the block in flight.
const SamplesPerBlock = 128

// BatchOptions configures a batch run.
type BatchOptions struct {
	// SamplesPerBlock overrides the 128-sample commit budget. Segments are
	// never split, so a block may exceed the budget by its last segment.
	SamplesPerBlock int
	// StopAfterBlock simulates a forced interruption after this many blocks.
	StopAfterBlock int
}

// BatchResult reports what a batch run produced.
type BatchResult struct {
	Segments       []Segment
	Fingerprints   []fingerprint.Fingerprint
	Decisions      []fingerprint.Decision
	ManifestSHA256 string
	Recovered      bool
	SkippedBlocks  int
	TornTail       bool
}

// RunBatch processes planned segments in blocks. Committed blocks are skipped
// on a resumed run, and the resulting manifest must equal a continuous run.
func RunBatch(planner *Planner, reqs []Request, store *checkpoint.Store, opt BatchOptions) (*BatchResult, error) {
	if opt.SamplesPerBlock <= 0 {
		opt.SamplesPerBlock = SamplesPerBlock
	}
	planned, err := planner.Plan(reqs)
	if err != nil && len(planned) == 0 {
		return nil, err
	}
	planErr := err
	res := &BatchResult{TornTail: store.Recovered()}
	var problems []error
	if planErr != nil {
		problems = append(problems, planErr)
	}

	blocks := planBlocks(planned, opt.SamplesPerBlock)
	done := 0
	for bi, block := range blocks {
		// Completion is decided per segment, not per block index: a resumed
		// run may group work differently and must still skip committed chunks.
		replayed, pending := splitCommitted(store, block)
		if len(pending) == 0 {
			res.SkippedBlocks++
			res.Recovered = true
			res.Segments = append(res.Segments, replayed...)
			continue
		}
		recs := make([]checkpoint.Record, 0, len(pending))
		res.Segments = append(res.Segments, replayed...)
		for _, s := range pending {
			seg, err := planner.Materialize(s)
			if err != nil {
				problems = append(problems, err)
				continue
			}
			res.Segments = append(res.Segments, seg)
			rec := recordOf(seg)
			rec.Block = bi
			if seg.Recovered {
				res.Recovered = true
			}
			recs = append(recs, rec)
		}
		if len(recs) > 0 {
			if err := store.Append(recs...); err != nil {
				return res, err
			}
			// Every committed block is durable: the journal append is fsynced
			// before the atomic checkpoint publish, so a crash loses at most
			// the block in flight.
			if err := store.Commit(); err != nil {
				return res, err
			}
		}
		done++
		if opt.StopAfterBlock > 0 && done >= opt.StopAfterBlock {
			return res, fault.Errorf("pipeline.batch", fault.KindCheckpoint,
				"forced interruption after %d blocks", done).Recovered()
		}
	}
	sortSegments(res.Segments)
	res.Decisions, err = fingerprint.Dedupe(toFingerprintSegments(res.Segments))
	if err != nil {
		return res, err
	}
	res.Fingerprints = uniqueFingerprints(res.Segments, res.Decisions)
	res.ManifestSHA256 = fingerprint.ManifestHash(res.Fingerprints)
	if len(problems) > 0 {
		return res, fault.New("pipeline.batch", fault.KindTruncated, errors.Join(problems...)).Recovered()
	}
	return res, nil
}

func recordOf(s Segment) checkpoint.Record {
	return checkpoint.Record{TrackID: s.TrackID, StartNS: s.StartNS, EndNS: s.EndNS,
		FirstSample: s.FirstSample, Samples: s.SampleCount, Content: s.ContentHex,
		SilentNS: s.SilentNS, KeyExact: s.KeyExact, Recovered: s.Recovered}
}

// splitCommitted separates planned segments with durable records from those
// still to be hashed. Replayed segments are rebuilt from the journal so their
// content hashes are exactly what was committed earlier.
func splitCommitted(store *checkpoint.Store, plan []SegmentPlan) ([]Segment, []SegmentPlan) {
	var done []Segment
	var pending []SegmentPlan
	for _, s := range plan {
		if !store.Has(s.TrackID, s.StartNS, s.EndNS, s.FirstSample) {
			pending = append(pending, s)
			continue
		}
		recs := store.Lookup(s.TrackID, s.StartNS, s.EndNS, s.FirstSample)
		seg, err := segmentFromRecord(recs, s)
		if err != nil {
			pending = append(pending, s)
			continue
		}
		done = append(done, seg)
	}
	return done, pending
}

func segmentFromRecord(recs []checkpoint.Record, plan SegmentPlan) (Segment, error) {
	if len(recs) == 0 {
		return Segment{}, fault.Errorf("pipeline.batch", fault.KindCheckpoint, "record disappeared during replay")
	}
	r := recs[0]
	seg := Segment{
		ID: segmentID(r.TrackID, r.FirstSample, r.Samples, r.StartNS), TrackID: r.TrackID,
		StartNS: r.StartNS, EndNS: r.EndNS, FirstSample: r.FirstSample,
		SampleCount: r.Samples, ContentHex: r.Content, SHA256: r.Content,
		SilentNS: r.SilentNS, KeyExact: r.KeyExact, Recovered: r.Recovered,
		KeyOffset: plan.KeyOffset, KeyPresented: plan.KeyPresented,
	}
	raw, err := hex.DecodeString(r.Content)
	if err != nil || len(raw) != 32 {
		return Segment{}, fault.Errorf("pipeline.batch", fault.KindCheckpoint,
			"committed record for %s has malformed content hash %q", seg.ID, r.Content).
			WithTrack(r.TrackID).Fatal()
	}
	copy(seg.Content[:], raw)
	return seg, nil
}

// segmentID names a slice by track, its sample span and requested window.
// Cuts that align to the same sync frame can legitimately overlap, so the
// window is part of the identity.
func segmentID(track uint32, first, count int, startNS int64) string {
	return fmt.Sprintf("t%02d-s%d-e%d@%d", track, first, first+count, startNS)
}

// planBlocks groups consecutive segments until the accumulated sample count
// reaches budget. Plans are deterministic, so a resumed run derives the same
// block boundaries and can match its journal against them.
func planBlocks(plans []SegmentPlan, budget int) [][]SegmentPlan {
	if budget <= 0 {
		budget = SamplesPerBlock
	}
	var blocks [][]SegmentPlan
	start, samples := 0, 0
	for i, p := range plans {
		samples += p.SampleCount
		if samples >= budget || i == len(plans)-1 {
			blocks = append(blocks, plans[start:i+1])
			start, samples = i+1, 0
		}
	}
	return blocks
}

func sortSegments(segs []Segment) {
	sort.Slice(segs, func(i, j int) bool {
		if segs[i].TrackID != segs[j].TrackID {
			return segs[i].TrackID < segs[j].TrackID
		}
		return segs[i].StartNS < segs[j].StartNS
	})
}

func toFingerprintSegments(segs []Segment) []fingerprint.Segment {
	out := make([]fingerprint.Segment, len(segs))
	for i, s := range segs {
		out[i] = fingerprint.Segment{TrackID: s.TrackID, StartNS: s.StartNS, EndNS: s.EndNS,
			FirstSample: s.FirstSample, SampleCount: s.SampleCount, Content: s.Content}
	}
	return out
}

func uniqueFingerprints(segs []Segment, decs []fingerprint.Decision) []fingerprint.Fingerprint {
	var out []fingerprint.Fingerprint
	for i, d := range decs {
		if d.Duplicate {
			continue
		}
		s := segs[i]
		out = append(out, fingerprint.New(s.TrackID, s.StartNS, s.EndNS, s.SampleCount, s.Content))
	}
	return out
}
