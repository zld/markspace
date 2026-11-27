package main

import (
	"tsrepair/internal/fixture"
	"tsrepair/internal/mediaerr"
	"tsrepair/internal/pipeline"
)

// rebuiltSegment is the recomputed hash for one segment position.
type rebuiltSegment struct {
	Representation int
	SegmentID      int
	SHA256         string
}

// rebuild regenerates the stream from the summary's seed and geometry, runs
// the same deterministic repair, and returns every recovered segment hash.
// Corruption modes are not part of the stored hash identity because the
// recovered segments are identical regardless of recoverable faults.
func rebuild(summary *Summary) ([]rebuiltSegment, error) {
	opts := fixture.DefaultOptions(int64(summary.Seed))
	opts.Segments = summary.SegPerRep
	opts.Representations = summary.Representations
	for _, c := range summary.Corruptions {
		opts.Corruptions = append(opts.Corruptions, fixture.Corruption(c))
	}
	reps, err := fixture.Build(opts)
	if err != nil {
		return nil, err
	}

	ec := &mediaerr.Collector{}
	cfg := pipeline.DefaultConfig()
	cfg.SamplesPerSegment = opts.SamplesPerSegment
	res, err := pipeline.Run(reps, cfg, ec)
	if err != nil {
		return nil, err
	}

	var out []rebuiltSegment
	for _, rpt := range res.Reports {
		out = append(out, rebuiltSegment{
			Representation: rpt.Segment.Representation,
			SegmentID:      rpt.Segment.ID,
			SHA256:         rpt.SHA256,
		})
	}
	return out, nil
}

func findHash(segs []rebuiltSegment, rep, id int) string {
	for _, s := range segs {
		if s.Representation == rep && s.SegmentID == id {
			return s.SHA256
		}
	}
	return ""
}
