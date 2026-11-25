package manifest

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Parse decodes a JSON material manifest and performs structural validation.
// A structurally decodable but corrupt sample table returns both the manifest
// and a *CorruptionError so callers can still emit an evidence report.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest JSON: %w", err)
	}
	if m.SchemaVersion == "" {
		m.SchemaVersion = "rtptrace.manifest/v1"
	}
	if m.Duration <= 0 {
		return nil, fmt.Errorf("manifest duration must be positive")
	}
	if m.SegmentDuration <= 0 {
		return nil, fmt.Errorf("manifest segment duration must be positive")
	}
	if m.SegmentDuration > m.Duration {
		return nil, fmt.Errorf("segment duration %s exceeds duration %s", m.SegmentDuration, m.Duration)
	}
	if len(m.Tracks) == 0 {
		return nil, fmt.Errorf("manifest has no tracks")
	}
	if len(m.Representations) == 0 && len(m.Bitrates) == 0 {
		return nil, fmt.Errorf("manifest has no target bitrates")
	}
	if len(m.Representations) == 0 {
		m.Representations = make([]RepresentationTarget, 0, len(m.Bitrates))
		for _, b := range m.Bitrates {
			m.Representations = append(m.Representations, RepresentationTarget{
				ID: BitrateID(b), Bitrate: b,
			})
		}
	}

	seenTrack := make(map[uint32]bool, len(m.Tracks))
	for ti := range m.Tracks {
		t := &m.Tracks[ti]
		if t.ID == 0 {
			return &m, fmt.Errorf("track at index %d has zero id", ti)
		}
		if seenTrack[t.ID] {
			return &m, fmt.Errorf("duplicate track id %d", t.ID)
		}
		seenTrack[t.ID] = true
		if t.SourceTimescale == 0 {
			return &m, fmt.Errorf("track %d has zero source timescale", t.ID)
		}
		if t.Kind != TrackVideo && t.Kind != TrackAudio {
			return &m, fmt.Errorf("track %d has unsupported kind %q", t.ID, t.Kind)
		}
		if err := validateSamples(t); err != nil {
			return &m, err
		}
	}
	return &m, nil
}

func validateSamples(t *Track) error {
	actual := uint32(len(t.Samples))
	declared := t.DeclaredSampleCount
	if declared == 0 {
		declared = actual
		t.DeclaredSampleCount = actual
	}
	if declared > actual {
		sample := actual + 1
		if t.FaultSampleHint != 0 { // populated by deterministic fixture input
			sample = t.FaultSampleHint
		}
		return &CorruptionError{
			Code: "truncated-stbl", TrackID: t.ID, Sample: sample, Recoverable: false,
			Message: fmt.Sprintf("stsz declares %d samples but only %d are present", declared, actual),
		}
	}
	if declared < actual {
		return &CorruptionError{
			Code: "invalid-stbl-count", TrackID: t.ID, Sample: declared + 1, Recoverable: false,
			Message: fmt.Sprintf("stsz count %d is smaller than %d parsed samples", declared, actual),
		}
	}
	var lastDTS int64
	for i := range t.Samples {
		want := uint32(i + 1)
		s := &t.Samples[i]
		if s.Index == 0 {
			s.Index = want
		}
		if s.Index != want {
			return &CorruptionError{
				Code: "nonsequential-sample", TrackID: t.ID, Sample: want, Recoverable: false,
				Message: fmt.Sprintf("sample index %d found at position %d", s.Index, want),
			}
		}
		if i > 0 && s.DecodeTicks < lastDTS {
			return &CorruptionError{
				Code: "dts-regression", TrackID: t.ID, Sample: s.Index, Recoverable: false,
				Message: fmt.Sprintf("decode timestamp %d follows %d", s.DecodeTicks, lastDTS),
			}
		}
		lastDTS = s.DecodeTicks
	}
	return nil
}

// BitrateID returns the stable representation identifier for a bitrate.
func BitrateID(bitsPerSecond int) string {
	if bitsPerSecond%1000 == 0 {
		return "rep-" + strconv.Itoa(bitsPerSecond/1000) + "k"
	}
	return "rep-" + strconv.Itoa(bitsPerSecond)
}

// CanonicalBitrates parses values such as 800k,1600k deterministically.
func CanonicalBitrates(values []string) ([]int, error) {
	out := make([]int, 0, len(values))
	for _, v := range values {
		mult := 1
		switch {
		case len(v) > 1 && (v[len(v)-1] == 'k' || v[len(v)-1] == 'K'):
			v = v[:len(v)-1]
			mult = 1000
		case len(v) > 1 && (v[len(v)-1] == 'm' || v[len(v)-1] == 'M'):
			v = v[:len(v)-1]
			mult = 1_000_000
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid bitrate %q", v)
		}
		out = append(out, n*mult)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one bitrate is required")
	}
	return out, nil
}
