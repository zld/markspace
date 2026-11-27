// Package pipeline wires manifest parsing, demuxing, clock recovery,
// fan-in cross-checks, track mapping, lossless remux and fingerprinting into
// one deterministic run. All fixtures are generated in-process; no external
// files or services are touched.
package pipeline

import (
	"math/rand"
	"time"

	"mediarc/internal/archive"
	"mediarc/internal/clock"
	"mediarc/internal/fingerprint"
	"mediarc/internal/model"
	"mediarc/internal/report"
	"mediarc/internal/track"
)

// Config mirrors the CLI demo flags plus internal recovery limits.
type Config struct {
	Seed       int64
	DriftMs    int64
	BoundaryMs int64
	BudgetMs   int64
	// SimulatedElapsed, when positive, replaces the wall-clock measurement so
	// budget verdicts stay deterministic on fast machines where the real run
	// finishes below the smallest millisecond budget. Zero means measure time.
	SimulatedElapsed time.Duration
}

// Scenario is one generated representation plus the synthetic fragment bytes
// its recovery runs against.
type Scenario struct {
	Rep       model.Representation
	Tracks    []model.Track
	Corrupt   bool
	Truncated bool
	// Wrap makes the fixture carry a 32-bit timestamp wraparound.
	Wrap bool
	// Drift forces a clock slope beyond the configured drift threshold.
	Drift bool
}

// Run executes the whole pipeline and returns the assembled report input.
func Run(cfg Config) (report.Summary, []model.RecoveredRepresentation, clock.FanInResult, time.Duration) {
	start := time.Now()
	rng := rand.New(rand.NewSource(cfg.Seed))

	scenarios := buildScenarios(cfg, rng)
	fanCfg := clock.FanoutConfig{MaxSamplesPerTrack: 100000, DriftThresholdMsPerSec: cfg.DriftMs}
	recovered := make([]model.RecoveredRepresentation, 0, len(scenarios))
	itemInputs := make([]report.ItemInput, 0, len(scenarios))
	reviewIDs := []string{}

	var sources []track.Source
	referenceID := ""

	for _, sc := range scenarios {
		rec := clock.RecoverRepresentation(sc.Rep, sc.Tracks, fanCfg)
		if sc.Corrupt || sc.Truncated {
			rec.OK = false
			rec.Errors = append(rec.Errors, model.New(model.KindSegmentCorrupt,
				sc.Rep.ID, "synthetic fragment failure"))
			for i := range rec.Tracks {
				rec.Tracks[i].Trusted = false
			}
		}
		recovered = append(recovered, rec)
		if rec.OK && referenceID == "" {
			referenceID = rec.ID
		}

		item := report.ItemInput{Rep: rec}
		// Lossless remux is verified on the original demuxed tracks: recovery
		// output (unwrapped timelines) never feeds the byte-exact container
		// round trip. Corruption stays isolated to this item.
		if len(sc.Tracks) > 0 && !sc.Corrupt && !sc.Truncated {
			remux := archive.Remux(sc.Rep.ID+".mp4", sc.Tracks)
			item.FingerprintMatch = remux.Lossless
			// A carrier-wrapping timeline cannot be expressed by the linear
			// tfdt of fragmented MP4: the mismatch is a known container limit,
			// so the item enters review rather than reporting corruption.
			if !remux.Lossless && sc.Wrap {
				item.ReviewRequired = true
				reviewIDs = append(reviewIDs, rec.ID)
			}
		}
		if !rec.OK {
			item.ReviewRequired = true
			reviewIDs = append(reviewIDs, rec.ID)
		}
		itemInputs = append(itemInputs, item)

		for _, rt := range rec.Tracks {
			role := roleFor(rt.Track.Codec)
			// Only the single healthy reference representation feeds the
			// explicit one-to-one output mapping; sibling representations are
			// cross-checked through the shared timeline instead of competing
			// for the same output slot.
			if rec.OK && rec.ID == referenceID && rt.Trusted && !rt.Track.NotMappable {
				sources = append(sources, track.Source{RepID: rec.ID, Role: role, Track: rt})
			}
		}
	}

	fanIn := clock.FanIn(recovered, clock.FanInConfig{BoundaryThresholdMs: cfg.BoundaryMs})

	// Explicit mapping: one video and one audio output, each fed exactly once
	// from the healthy reference representation. MPEG-TS tracks share the
	// single 90 kHz transport clock, so both outputs declare that time base.
	plan := track.Plan{
		Outputs: []track.OutputSpec{
			{OutputID: 1, Role: "video", Timescale: 90000},
			{OutputID: 2, Role: "audio", Timescale: 90000},
		},
		Sources: sources,
	}
	mapping := plan.Submit()
	for i := range itemInputs {
		// Only the reference representation participates in the explicit
		// mapping, so its item alone carries the mapping verdict.
		if recovered[i].ID == referenceID {
			itemInputs[i].MappingParticipant = true
			itemInputs[i].Mapped = mapping.Accepted
		}
	}

	// Fingerprints per representation against the shared boundary set; only
	// trusted digests are allowed to dedup, the rest enter manual review.
	digests := map[string]fingerprint.Digest{}
	for _, rec := range recovered {
		nt, trusted := fingerprint.Normalize(nil, rec)
		var bounds []model.Boundary
		for _, t0 := range rec.Tracks {
			if t0.Trusted {
				bounds = t0.Boundaries
				break
			}
		}
		digests[rec.ID] = fingerprint.Hash(nt, bounds, trusted)
	}
	_, review := fingerprint.Dedup(repIDs(recovered), digests)
	for _, rv := range review {
		reviewIDs = append(reviewIDs, rv.RepID)
	}

	elapsed := time.Since(start)
	if cfg.SimulatedElapsed > 0 {
		elapsed = cfg.SimulatedElapsed
	}
	in := report.Input{
		Seed:       cfg.Seed,
		Thresholds: report.Thresholds{BoundaryMs: cfg.BoundaryMs, DriftMsPerSec: cfg.DriftMs, BudgetMs: cfg.BudgetMs},
		Elapsed:    elapsed,
		Items:      itemInputs,
		FanIn:      report.NewFanInView(fanIn.Anomalies, fanIn.Untrusted),
		ReviewIDs:  unique(reviewIDs),
	}
	return report.Build(in), recovered, fanIn, elapsed
}

func repIDs(reps []model.RecoveredRepresentation) []string {
	out := make([]string, len(reps))
	for i, r := range reps {
		out[i] = r.ID
	}
	return out
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func roleFor(c model.Codec) string {
	switch c {
	case model.CodecAVC1:
		return "video"
	case model.CodecMP4A:
		return "audio"
	default:
		return "data"
	}
}
