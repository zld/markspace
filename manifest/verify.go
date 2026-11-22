package manifest

import (
	"encoding/json"

	"mediacore/media"
)

// Tolerance distinguishes hard failures (damaged material or wrong stats) from
// parameter problems.
type Failure struct {
	Category media.Category `json:"category"`
	Field    string         `json:"field"`
	Expected string         `json:"expected"`
	Actual   string         `json:"actual"`
	Detail   string         `json:"detail,omitempty"`
}

// VerifyReport is the outcome of an independent manifest check.
type VerifyReport struct {
	OK            bool      `json:"ok"`
	Recomputed    []Check   `json:"recomputed"`
	TimingChecked bool      `json:"timing_checked"`
	AlignmentMS   int64     `json:"alignment_error_ms"`
	Failures      []Failure `json:"failures,omitempty"`
	Warnings      []string  `json:"warnings,omitempty"`
}

// Check is one recomputation from manifest entries.
type Check struct {
	Name       string `json:"name"`
	Stored     string `json:"stored"`
	Recomputed string `json:"recomputed"`
	Within     bool   `json:"within"`
	Note       string `json:"note,omitempty"`
}

// VerifyOptions tunes verification.
type VerifyOptions struct {
	// MaxStatPct is the allowed relative error for count/rate recomputation.
	MaxStatPct float64
	// MaxAlignmentMS is the allowed media/MP4 timing disagreement.
	MaxAlignmentMS int64
}

// DefaultVerifyOptions allows 2% recomputation error and 2 ms alignment.
func DefaultVerifyOptions() VerifyOptions {
	return VerifyOptions{MaxStatPct: 2.0, MaxAlignmentMS: 2}
}

// Parse decodes a manifest document.
func Parse(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, media.Wrap(media.CatManifestMismatch, media.StageVerify, err, "parse manifest")
	}
	return &m, nil
}

// Verify recomputes frames, segments, duration, gaps, silence and bitrate from
// the manifest's own entries and compares them to the stored statistics. It
// also checks the manifest digest and time monotonicity.
func Verify(m *Manifest, opts VerifyOptions) VerifyReport {
	rep := VerifyReport{OK: true}
	add := func(c Check) {
		rep.Recomputed = append(rep.Recomputed, c)
		if !c.Within {
			rep.OK = false
			rep.Failures = append(rep.Failures, Failure{
				Category: media.CatManifestMismatch,
				Field:    c.Name,
				Expected: c.Stored,
				Actual:   c.Recomputed,
				Detail:   c.Note,
			})
		}
	}
	if !m.IndexComplete {
		rep.OK = false
		rep.Failures = append(rep.Failures, Failure{
			Category: media.CatIndexTruncated, Field: "index_complete",
			Expected: "true", Actual: "false",
			Detail: "manifest was built from an incomplete index",
		})
	}
	if m.Version != ManifestVersion {
		rep.OK = false
		rep.Failures = append(rep.Failures, Failure{
			Category: media.CatIndexVersion, Field: "version",
			Expected: itoa(int64(ManifestVersion)), Actual: itoa(int64(m.Version)),
		})
	}
	// Recompute aggregates from entries.
	var frames, bytes, gaps, missing, dupFrames, silence, duration int64
	var prevEnd int64
	monotonic := true
	for _, s := range m.Segments {
		frames += int64(s.FrameCount)
		bytes += s.Bytes
		gaps += int64(s.GapCount)
		missing += int64(s.MissingFrames)
		dupFrames += int64(s.DuplicateFrames)
		silence += s.SilenceMS
		if s.EndMS < s.StartMS {
			monotonic = false
		}
		if s.StartMS < prevEnd {
			monotonic = false
		}
		prevEnd = s.EndMS
	}
	if len(m.Segments) > 0 {
		duration = m.Segments[len(m.Segments)-1].EndMS - m.Segments[0].StartMS
	}
	add(intCheck("frame_count", int64(m.Stats.FrameCount), frames, 0))
	add(intCheck("segment_count", int64(m.Stats.SegmentCount), int64(len(m.Segments)), 0))
	add(intCheck("duration_ms", m.Stats.DurationMS, duration, 0))
	add(intCheck("gap_count", int64(m.Stats.GapCount), gaps, 0))
	add(intCheck("missing_frames", int64(m.Stats.MissingFrames), missing, 0))
	add(intCheck("duplicate_group_count", int64(m.Stats.DuplicateGroups), int64(len(m.DuplicateGroups)), 0))
	add(intCheck("duplicate_frames", int64(m.Stats.DuplicateFrames), dupFrames, 0))
	add(intCheck("silence_ms", m.Stats.SilenceConfirmedMS, silence, 0))
	// Bitrate is recomputed from bytes and duration with a percentage tolerance.
	actualRecomputed, devRecomputed := rates(bytes, duration, m.Stats.TargetKBPS)
	add(pctCheck("actual_kbps", int64(m.Stats.ActualKBPS), int64(actualRecomputed), opts.MaxStatPct))
	add(pctCheck("deviation_pct", int64(m.Stats.DeviationPct*100), int64(devRecomputed*100), opts.MaxStatPct))
	add(boolCheck("time_monotonic", monotonic))
	// The manifest digest must reproduce.
	if m.ComputeDigest() != m.ManifestDigest {
		rep.OK = false
		rep.Failures = append(rep.Failures, Failure{
			Category: media.CatManifestMismatch, Field: "manifest_digest",
			Expected: m.ManifestDigest, Actual: m.ComputeDigest(),
			Detail: "manifest content does not reproduce its digest",
		})
	}
	// Duplicate group frames should reference distinct sequences.
	for _, g := range m.DuplicateGroups {
		if len(g.Frames) != g.Count {
			rep.OK = false
			rep.Failures = append(rep.Failures, Failure{
				Category: media.CatManifestMismatch, Field: "duplicate_group",
				Expected: itoa(int64(g.Count)), Actual: itoa(int64(len(g.Frames))),
				Detail: "group count disagrees with its frames",
			})
		}
	}
	// Timing cross-check against the MP4-bound values.
	if m.Timing.Checked {
		rep.TimingChecked = true
		rep.AlignmentMS = m.Timing.AlignmentErrorMS
		limit := opts.MaxAlignmentMS
		if limit <= 0 {
			limit = DefaultVerifyOptions().MaxAlignmentMS
		}
		within := m.Timing.AlignmentErrorMS <= limit
		rep.Recomputed = append(rep.Recomputed, Check{
			Name:       "alignment_error_ms",
			Stored:     itoa(limit),
			Recomputed: itoa(m.Timing.AlignmentErrorMS),
			Within:     within,
		})
		if !within {
			rep.OK = false
			rep.Failures = append(rep.Failures, Failure{
				Category: media.CatTimeDiscontinuity, Field: "alignment_error_ms",
				Expected: "<=" + itoa(limit), Actual: itoa(m.Timing.AlignmentErrorMS),
				Detail: "media clock and MP4 sample timing disagree",
			})
		}
	} else {
		rep.Warnings = append(rep.Warnings, "manifest has no MP4 timing binding")
	}
	if m.Stats.SilenceUnknownMS > 0 {
		rep.Warnings = append(rep.Warnings, "manifest contains unknown silence intervals")
	}
	return rep
}

// Failed reports whether the report has hard failures.
func (r VerifyReport) Failed() bool { return !r.OK }

func intCheck(name string, stored, recomputed, tol int64) Check {
	diff := stored - recomputed
	if diff < 0 {
		diff = -diff
	}
	return Check{Name: name, Stored: itoa(stored), Recomputed: itoa(recomputed), Within: diff <= tol}
}

func pctCheck(name string, stored, recomputed int64, maxPct float64) Check {
	base := recomputed
	if base < 0 {
		base = -base
	}
	diff := stored - recomputed
	if diff < 0 {
		diff = -diff
	}
	within := true
	if base == 0 {
		within = diff == 0
	} else {
		within = float64(diff)/float64(base)*100 <= maxPct
	}
	return Check{Name: name, Stored: itoa(stored), Recomputed: itoa(recomputed), Within: within,
		Note: itoa(int64(maxPct*100)) + "_pct_tolerance"}
}

func boolCheck(name string, ok bool) Check {
	s := "true"
	if !ok {
		s = "false"
	}
	return Check{Name: name, Stored: s, Recomputed: s, Within: ok}
}

// Itoa renders an int64 without importing strconv into hot paths.
func Itoa(v int64) string { return itoa(v) }

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
