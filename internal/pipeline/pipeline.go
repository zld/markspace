// Package pipeline orchestrates the full mediamerge flow: fixture inputs are
// parsed onto the shared timebase, mapped through the explicit version table,
// merged under the chosen policy, segmented, content-hashed and committed via
// the write-ahead transaction journal.
package pipeline

import (
	"mediamerge/internal/fault"
	"mediamerge/internal/fixture"
	"mediamerge/internal/mapping"
	"mediamerge/internal/merge"
	"mediamerge/internal/report"
	"mediamerge/internal/track"
)

// Policy names re-exported for the CLI.
const (
	PolicyStrict   = string(merge.PolicyStrict)
	PolicyMarkGaps = string(merge.PolicyMarkGaps)
)

// Config controls one verify run.
type Config struct {
	Case           string
	OutDir         string
	Policy         string
	InterruptAfter int // 0 means run to completion
}

// Run executes a verify scenario and returns its report metrics.
func Run(cfg Config) (*report.Metrics, error) {
	m := &report.Metrics{Case: cfg.Case, ManifestChanged: true}
	bundle, err := fixture.Generate(cfg.OutDir, fixture.Case(cfg.Case))
	if err != nil {
		return m, fault.New(fault.StageIO, fault.KindIO, cfg.OutDir, err.Error())
	}
	lenient := cfg.Policy == PolicyMarkGaps

	// --- parse subtitles onto the shared 90kHz timebase ---
	srtTrack, srtRej, err := parseSRT(bundle, lenient)
	if err != nil {
		return m, err
	}
	vttTrack, vttRej, err := parseVTT(bundle, lenient)
	if err != nil {
		return m, err
	}

	// --- explicit version/track mapping ---
	table, err := mapping.NewTable([]mapping.Rule{
		{Source: "subtitles.v1.srt", SourceVersion: "v1", TargetTrack: "timeline-en",
			Language: "en", Enabled: true},
		{Source: "subtitles.v2.vtt", SourceVersion: "v2", TargetTrack: "timeline-en",
			Language: "en", Enabled: true},
	})
	if err != nil {
		return m, err
	}
	mapped, err := mapTracks(table, srtTrack, vttTrack)
	if err != nil {
		return m, err
	}

	// --- merge under policy ---
	policy := merge.Policy(cfg.Policy)
	res, err := merge.Merge(mapped, policy)
	if err != nil {
		// Strict conflict: nothing is committed, so the prior manifest (if
		// any) stays byte-for-byte unchanged.
		m.FromMerge(res)
		m.Conflicts = len(res.Conflicts)
		m.Rollbacks = 1
		m.ManifestChanged = false
		return m, err
	}

	// --- rejected cues from lenient parsing ---
	for _, rc := range append(srtRej, vttRej...) {
		res.Rejected = append(res.Rejected, track.Rejected{
			Source: rc.Source, Line: rc.Line, Reason: rc.Detail})
	}
	// Coverage gaps are derived by the merger itself: the DF-rejected SRT cue
	// leaves a range the other source still covers, producing one provenance
	// gap. The truncated tail's damage flag stays recorded on the bundle.
	m.FromMerge(res)
	m.Rejected = len(res.Rejected)
	m.Gaps = len(res.Gaps)
	// Roundtrip counts the cues that survived parsing on the primary (SRT)
	// source. A DF-rejected cue is excluded here and counted under Rejected;
	// the bare-index truncated tail is not a cue at all, its hole is a Gap.
	m.RoundtripOK = len(srtTrack.Cues)
	m.RoundtripTotal = len(srtTrack.Cues)

	// --- MP4 keyframe index ---
	hits, total, err := keyframeStats(bundle)
	if err != nil {
		return m, err
	}
	m.Keyframe(hits, total)

	// --- RTP sideband validation (anomalies would be counted here) ---
	if err := checkRTP(bundle); err != nil {
		return m, err
	}

	// --- segment, hash and transactionally commit ---
	if err := commitSegments(cfg, m, res); err != nil {
		m.Rollbacks = 1
		if reason := rollbackReason(cfg.OutDir); reason != "" {
			m.RollbackReason = reason
		}
		m.ManifestChanged = false
		return m, err
	}
	return m, nil
}
