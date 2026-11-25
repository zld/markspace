package manifest

import (
	"fmt"
	"sort"

	"github.com/zld/media-regress/internal/error"
)

// Verify checks a built manifest and returns any continuity or digest errors.
// It never repairs anything: every defect is reported with the track, the
// segment and the tick where it was observed, and the caller exits non-zero.
func Verify(m *Manifest, expected AudioTarget, recomputeDigest bool) []error {
	var errs []error
	if m.Version != Version {
		errs = append(errs, merr.New(merr.StageManifest, merr.KindInconsistent, 0, 0,
			"manifest version %d is not %d", m.Version, Version))
	}
	if m.InputDigest == "" {
		errs = append(errs, merr.New(merr.StageManifest, merr.KindDigest, 0, 0,
			"manifest has no input digest"))
	}
	if recomputeDigest {
		stored := m.Digest
		if err := m.Finalize(); err != nil {
			errs = append(errs, err)
		} else if m.Digest != stored {
			errs = append(errs, merr.New(merr.StageManifest, merr.KindDigest, 0, 0,
				"manifest digest %s does not match content %s", stored, m.Digest))
			m.Digest = stored
		}
	}
	errs = append(errs, verifyRepresentations(m)...)
	errs = append(errs, verifyAudioTrack(m, expected)...)
	return errs
}

func verifyRepresentations(m *Manifest) []error {
	var errs []error
	if len(m.Representations) == 0 {
		return []error{merr.New(merr.StageManifest, merr.KindContinuity, 0, 0,
			"manifest has no representations")}
	}
	seen := map[string]bool{}
	for _, r := range m.Representations {
		if seen[r.ID] {
			errs = append(errs, merr.New(merr.StageManifest, merr.KindInconsistent, r.TrackID, 0,
				"representation %s appears more than once", r.ID))
		}
		seen[r.ID] = true
		if r.EndTick <= r.StartTick {
			errs = append(errs, merr.New(merr.StageManifest, merr.KindContinuity, r.TrackID, r.StartTick,
				"representation %s window [%d,%d) is empty or inverted", r.ID, r.StartTick, r.EndTick))
		}
		if r.StartTick < 0 {
			errs = append(errs, merr.New(merr.StageManifest, merr.KindContinuity, r.TrackID, r.StartTick,
				"representation %s starts before the master origin", r.ID))
		}
		if r.Timescale == 0 {
			errs = append(errs, merr.New(merr.StageManifest, merr.KindInconsistent, r.TrackID, 0,
				"representation %s has zero timescale", r.ID))
		}
		if r.Render.Width == 0 || r.Render.Height == 0 {
			errs = append(errs, merr.New(merr.StageManifest, merr.KindInconsistent, r.TrackID, 0,
				"representation %s has no render dimensions", r.ID))
		}
		if r.Render.Rotation%90 != 0 || r.Render.Rotation < 0 || r.Render.Rotation > 270 {
			errs = append(errs, merr.New(merr.StageManifest, merr.KindInconsistent, r.TrackID, 0,
				"representation %s has invalid rotation %d", r.ID, r.Render.Rotation))
		}
	}
	return errs
}

func verifyAudioTrack(m *Manifest, expected AudioTarget) []error {
	var errs []error
	t := m.Track
	if t.ID == 0 {
		return []error{merr.New(merr.StageManifest, merr.KindInconsistent, 0, 0,
			"manifest has no audio track")}
	}
	if t.Kind != "audio" {
		errs = append(errs, merr.New(merr.StageManifest, merr.KindInconsistent, t.ID, 0,
			"audio track kind is %q", t.Kind))
	}
	if t.Timescale != expected.SampleRate {
		errs = append(errs, merr.New(merr.StageManifest, merr.KindInconsistent, t.ID, 0,
			"audio timescale %d does not match target sample rate %d", t.Timescale, expected.SampleRate))
	}
	if t.Audio.SampleRate != expected.SampleRate {
		errs = append(errs, merr.New(merr.StageManifest, merr.KindInconsistent, t.ID, 0,
			"published sample rate %d does not match target %d", t.Audio.SampleRate, expected.SampleRate))
	}
	if t.Audio.Layout != expected.Layout {
		errs = append(errs, merr.New(merr.StageManifest, merr.KindInconsistent, t.ID, 0,
			"published layout %q does not match target %q", t.Audio.Layout, expected.Layout))
	}
	if t.Audio.Channels != expected.Channels {
		errs = append(errs, merr.New(merr.StageManifest, merr.KindInconsistent, t.ID, 0,
			"published channel count %d does not match target %d", t.Audio.Channels, expected.Channels))
	}
	if t.Audio.LoudnessLUFS != expected.LoudnessLUFS {
		errs = append(errs, merr.New(merr.StageManifest, merr.KindInconsistent, t.ID, 0,
			"published loudness %q does not match target %q", t.Audio.LoudnessLUFS, expected.LoudnessLUFS))
	}
	if len(t.Segments) == 0 {
		return append(errs, merr.New(merr.StageManifest, merr.KindContinuity, t.ID, 0,
			"audio track has no segments"))
	}
	segs := append([]Segment(nil), t.Segments...)
	sort.Slice(segs, func(i, j int) bool { return segs[i].Index < segs[j].Index })
	for i, s := range segs {
		if s.TrackID != t.ID {
			errs = append(errs, merr.New(merr.StageManifest, merr.KindInconsistent, s.TrackID, s.StartTick,
				"segment %d belongs to track %d, not %d", s.Index, s.TrackID, t.ID))
		}
		if s.Index != i+1 {
			errs = append(errs, merr.New(merr.StageManifest, merr.KindContinuity, s.TrackID, s.StartTick,
				"segment numbering jumps: expected %d, found %d", i+1, s.Index))
		}
		if s.Frames <= 0 {
			errs = append(errs, merr.New(merr.StageManifest, merr.KindInconsistent, s.TrackID, s.StartTick,
				"segment %d has no frames", s.Index))
		}
		if s.Digest == "" {
			errs = append(errs, merr.New(merr.StageManifest, merr.KindDigest, s.TrackID, s.StartTick,
				"segment %d has no digest", s.Index))
		}
		if s.EndTick <= s.StartTick {
			errs = append(errs, merr.New(merr.StageManifest, merr.KindContinuity, s.TrackID, s.StartTick,
				"segment %d window [%d,%d) is empty", s.Index, s.StartTick, s.EndTick))
		}
		if i > 0 {
			prev := segs[i-1]
			if s.StartTick != prev.EndTick {
				errs = append(errs, merr.New(merr.StageManifest, merr.KindContinuity, s.TrackID, s.StartTick,
					"segment %d starts at %d but segment %d ended at %d (%s)",
					s.Index, s.StartTick, prev.Index, prev.EndTick, joinGap(s.StartTick, prev.EndTick)))
			}
		}
	}
	if t.StartTick > segs[0].StartTick {
		errs = append(errs, merr.New(merr.StageManifest, merr.KindContinuity, t.ID, t.StartTick,
			"track starts at %d but its first segment starts at %d", t.StartTick, segs[0].StartTick))
	}
	if t.EndTick < segs[len(segs)-1].EndTick {
		errs = append(errs, merr.New(merr.StageManifest, merr.KindContinuity, t.ID, t.EndTick,
			"track ends at %d before its last segment ends at %d", t.EndTick, segs[len(segs)-1].EndTick))
	}
	if t.StartTick < 0 || t.EndTick <= t.StartTick {
		errs = append(errs, merr.New(merr.StageManifest, merr.KindContinuity, t.ID, t.StartTick,
			"audio track window [%d,%d) is invalid", t.StartTick, t.EndTick))
	}
	return errs
}

func joinGap(start, prevEnd int64) string {
	switch {
	case start > prevEnd:
		return fmt.Sprintf("gap of %d ticks", start-prevEnd)
	case start < prevEnd:
		return fmt.Sprintf("overlap of %d ticks", prevEnd-start)
	default:
		return "adjacent"
	}
}

// DigestSummary turns verification errors into a terminal line.
func DigestSummary(m *Manifest) string {
	return fmt.Sprintf("manifest_segments=%d continuity_errors=%d sha256=%s",
		len(m.Track.Segments), m.Summary.ContinuityErrors, m.Digest)
}
