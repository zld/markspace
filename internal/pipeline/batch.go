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

// BlockSize is the number of segments committed per checkpoint block.
const BlockSize = 128

// BatchOptions configures a batch run.
type BatchOptions struct {
	BlockSize      int // defaults to 128
	StopAfterBlock int // >0 simulates a forced interruption after this many blocks
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
	if opt.BlockSize <= 0 {
		opt.BlockSize = BlockSize
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

	blocks := chunk(planned, opt.BlockSize)
	done := 0
	for bi, block := range blocks {
		if store.Completed(bi) {
			res.SkippedBlocks++
			res.Recovered = true
			segs, err := segmentsFromRecords(store, bi, block)
			if err != nil {
				return res, err
			}
			res.Segments = append(res.Segments, segs...)
			continue
		}
		recs := make([]checkpoint.Record, 0, len(block))
		for _, s := range block {
			seg, err := planner.Materialize(s)
			if err != nil {
				problems = append(problems, err)
				continue
			}
			res.Segments = append(res.Segments, seg)
			rec := recordOf(seg)
			rec.Block = bi
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
		SilentNS: s.SilentNS, KeyExact: s.KeyExact}
}

func segmentsFromRecords(store *checkpoint.Store, block int, plan []SegmentPlan) ([]Segment, error) {
	recs := store.BlockRecords(block)
	if len(recs) == 0 {
		return nil, fault.Errorf("pipeline.batch", fault.KindCheckpoint, "block %d marked complete but empty", block).Fatal()
	}
	byKey := map[string]SegmentPlan{}
	for _, s := range plan {
		byKey[planKey(s)] = s
	}
	out := make([]Segment, 0, len(recs))
	for _, r := range recs {
		seg := Segment{
			ID: segmentID(r.TrackID, r.FirstSample, r.Samples), TrackID: r.TrackID,
			StartNS: r.StartNS, EndNS: r.EndNS, FirstSample: r.FirstSample,
			SampleCount: r.Samples, ContentHex: r.Content, SHA256: r.Content,
			SilentNS: r.SilentNS, KeyExact: r.KeyExact,
		}
		if raw, err := hex.DecodeString(r.Content); err == nil && len(raw) == 32 {
			copy(seg.Content[:], raw)
		} else {
			return nil, fault.Errorf("pipeline.batch", fault.KindCheckpoint,
				"committed record for %s has malformed content hash %q", seg.ID, r.Content).
				WithTrack(r.TrackID).Fatal()
		}
		if s, ok := byKey[recordKey(r)]; ok {
			seg.KeyOffset, seg.KeyPresented = s.KeyOffset, s.KeyPresented
		}
		out = append(out, seg)
	}
	return out, nil
}

// segmentID names a slice by track and its sample span. Cuts that align to
// the same sync frame can legitimately overlap, so the end is part of the ID.
func segmentID(track uint32, first, count int) string {
	return fmt.Sprintf("t%02d-s%d-e%d", track, first, first+count)
}

func planKey(p SegmentPlan) string {
	return fmt.Sprintf("%d|%d|%d|%d", p.TrackID, p.StartNS, p.EndNS, p.FirstSample)
}

func recordKey(r checkpoint.Record) string {
	return fmt.Sprintf("%d|%d|%d|%d", r.TrackID, r.StartNS, r.EndNS, r.FirstSample)
}

func chunk[T any](in []T, n int) [][]T {
	var out [][]T
	for len(in) > 0 {
		k := n
		if k > len(in) {
			k = len(in)
		}
		out = append(out, in[:k])
		in = in[k:]
	}
	return out
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
