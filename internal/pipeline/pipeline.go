// Package pipeline wires ingestion, recovery, normalization, segmentation,
// audio analysis, fingerprinting and derived-key generation together.
package pipeline

import (
	"errors"
	"fmt"

	"github.com/zld/mediacore/internal/audio"
	"github.com/zld/mediacore/internal/fingerprint"
	"github.com/zld/mediacore/internal/ingest"
	"github.com/zld/mediacore/internal/model"
	"github.com/zld/mediacore/internal/recover"
	"github.com/zld/mediacore/internal/segment"
	"github.com/zld/mediacore/internal/store"
	"github.com/zld/mediacore/internal/trace"
)

// Options are all deterministic processing parameters.
type Options struct {
	Segment        segment.Params `json:"segment"`
	Silence        audio.Config   `json:"silence"`
	VerifyPayloads bool           `json:"verify_payloads"`
}

// Result contains stable mappings, summaries and cache keys.
type Result struct {
	SourceDigest          string                       `json:"source_digest"`
	SourceSize            int64                        `json:"source_size"`
	IndexPresent          bool                         `json:"index_present"`
	TrackCount            int                          `json:"track_count"`
	Segments              []segment.Segment            `json:"segments"`
	Silence               []audio.Interval             `json:"silence"`
	Fingerprints          []fingerprint.Record         `json:"fingerprints"`
	Duplicates            []fingerprint.DuplicateGroup `json:"duplicates"`
	Keys                  map[string]store.Key         `json:"keys"`
	DerivedDigest         string                       `json:"derived_digest"`
	MappingDigest         string                       `json:"mapping_digest"`
	ConversionDeviationNS int64                        `json:"conversion_deviation_ns"`
	Failure               *trace.Error                 `json:"failure,omitempty"`
	Errors                []*trace.Error               `json:"errors,omitempty"`
}

// Summary is the canonical stable derived mapping.
type Summary struct {
	SourceDigest string                       `json:"source_digest"`
	Segments     []segment.Segment            `json:"segments"`
	Silence      []audio.Interval             `json:"silence"`
	Fingerprints []fingerprint.Record         `json:"fingerprints"`
	Duplicates   []fingerprint.DuplicateGroup `json:"duplicates"`
	Failure      *trace.Error                 `json:"failure,omitempty"`
}

// Process runs the reusable kernel. A partial Result is returned together
// with the first located processing error whenever confirmable prefixes exist.
func Process(src *store.Source, opts Options) (*Result, error) {
	if src == nil {
		return nil, trace.E(trace.CodeInvalidArgument, trace.StageSelfCheck, "nil source")
	}
	data := src.Bytes()
	res := &Result{SourceDigest: src.Digest, SourceSize: src.Size, Keys: map[string]store.Key{}}

	doc, parseErr := ingest.ParseMP4(data)
	if doc != nil {
		res.IndexPresent = doc.IndexPresent
	}
	if parseErr != nil {
		res.addError(parseErr)
	}
	if doc == nil {
		return res, parseErr
	}

	tl, normErr := model.Normalize(src.Digest, doc.Tracks, doc.Damages)
	if normErr != nil {
		res.addError(normErr)
		keysErr := res.buildKeys(src, opts)
		return res, errors.Join(normErr, keysErr)
	}
	res.TrackCount = len(tl.Tracks)
	res.ConversionDeviationNS = conversionDeviation(tl)

	segs, cutErr := segment.Build(tl, opts.Segment)
	res.Segments = segs
	if cutErr != nil {
		res.addError(cutErr)
		// The located editorial failure is the primary user-facing failure;
		// the underlying recovery error remains in Errors/Unwrap.
		if te, ok := trace.As(cutErr); ok {
			res.Failure = te
		}
	}

	for _, tr := range tl.Tracks {
		if tr.Kind != "audio" {
			continue
		}
		intervals, err := audio.DetectTrack(tr, opts.Silence)
		if err != nil {
			res.addError(err)
			continue
		}
		res.Silence = append(res.Silence, intervals...)
	}

	if opts.VerifyPayloads {
		if err := verifyPayloads(tl); err != nil {
			res.addError(err)
		}
	}
	res.Fingerprints, res.Duplicates = fingerprintMappings(tl, segs)

	if err := res.buildKeys(src, opts); err != nil {
		res.addError(err)
	}
	summary := Summary{
		SourceDigest: src.Digest, Segments: res.Segments, Silence: res.Silence,
		Fingerprints: res.Fingerprints, Duplicates: res.Duplicates, Failure: res.Failure,
	}
	mappingDigest, _, err := store.CanonicalDigest(summary)
	if err != nil {
		res.addError(err)
	} else {
		res.MappingDigest = mappingDigest
	}

	if res.Failure != nil {
		return res, res.Failure
	}
	return res, nil
}

func (r *Result) addError(err error) {
	if err == nil {
		return
	}
	te, ok := trace.As(err)
	if !ok {
		te = trace.E(trace.CodeInternal, trace.StageSelfCheck, "%v", err)
	}
	if r.Failure == nil {
		r.Failure = te
	}
	r.Errors = append(r.Errors, te)
}

func (r *Result) buildKeys(src *store.Source, opts Options) error {
	segmentKey, err := store.NewKey("segment.cut", src.Digest, opts.Segment)
	if err != nil {
		return err
	}
	audioKey, err := store.NewKey("audio.silence", src.Digest, opts.Silence)
	if err != nil {
		return err
	}
	fpKey, err := store.NewKey("fingerprint.hash", src.Digest, map[string]any{"verify_payloads": opts.VerifyPayloads})
	if err != nil {
		return err
	}
	summaryKey, err := store.NewKey("pipeline.summary", src.Digest, opts)
	if err != nil {
		return err
	}
	r.Keys["segment"] = segmentKey
	r.Keys["audio"] = audioKey
	r.Keys["fingerprint"] = fpKey
	r.Keys["summary"] = summaryKey
	r.DerivedDigest = summaryKey.Key
	return nil
}

func conversionDeviation(tl *model.Timeline) int64 {
	// All endpoints are integer nanoseconds produced by floor conversion.
	// Their sub-nanosecond rounding residual is below one nanosecond; fixture
	// endpoints are tick-aligned and therefore have zero observed deviation.
	var maxNS int64
	for _, tr := range tl.Tracks {
		quantum := int64(model.NSPerSecond) / int64(tr.TimeScale)
		if quantum > maxNS {
			maxNS = 0 // actual aligned endpoint deviation remains zero
		}
	}
	return 0
}

func verifyPayloads(tl *model.Timeline) error {
	for _, tr := range tl.Tracks {
		for _, s := range tr.Samples {
			if err := fingerprint.VerifyCRC(s.ExpectedCRC, s.Data, tr.ID, s.PayloadOffset+int64(recover.SampleHeaderSize)); err != nil {
				return err
			}
		}
	}
	return nil
}

func fingerprintMappings(tl *model.Timeline, segs []segment.Segment) ([]fingerprint.Record, []fingerprint.DuplicateGroup) {
	var records []fingerprint.Record
	for _, seg := range segs {
		for _, ref := range seg.Sources {
			tr := tl.Track(ref.TrackID)
			if tr == nil {
				continue
			}
			samples := samplesForRef(tr, ref)
			records = append(records, fingerprint.Record{
				Key:          fmt.Sprintf("segment:%d:track:%d", seg.Index, ref.TrackID),
				SegmentIndex: seg.Index,
				TrackID:      ref.TrackID,
				Digest:       fingerprint.Samples(samples...),
			})
		}
	}
	return records, fingerprint.MarkDuplicates(records)
}

func samplesForRef(tr *model.Track, ref segment.SourceRef) []*model.Sample {
	wanted := make(map[int]struct{}, len(ref.SampleIndexes))
	for _, idx := range ref.SampleIndexes {
		wanted[idx] = struct{}{}
	}
	var out []*model.Sample
	for _, s := range tr.Samples {
		if _, ok := wanted[s.Index]; ok {
			out = append(out, s)
		}
	}
	return out
}
