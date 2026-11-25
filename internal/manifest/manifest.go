// Package manifest renders and verifies the delivery manifest: one entry per
// segment with its tick range, source and derived digests, byte count, quality
// and triggering event. Validation re-derives every digest and re-sums every
// total, so a tampered or stale manifest cannot pass.
package manifest

import (
	"fmt"

	"replaycheck/internal/model"
	"replaycheck/internal/policy"
	"replaycheck/internal/split"
	"replaycheck/internal/timeline"
)

// Entry is one delivered segment.
type Entry struct {
	Index         int        `json:"index"`
	StartTick     int64      `json:"startTick"`
	EndTick       int64      `json:"endTick"`
	SourceDigest  string     `json:"sourceDigest"`
	DerivedDigest string     `json:"derivedDigest"`
	Bytes         int64      `json:"bytes"`
	QualityPPM    policy.PPM `json:"qualityPpm"`
	Level         string     `json:"level"`
	Triggers      []string   `json:"triggers"`
	Transitions   []string   `json:"transitions,omitempty"`
	SampleCount   int        `json:"sampleCount"`
}

// Manifest is the delivered document.
type Manifest struct {
	Version       int        `json:"version"`
	Fixture       string     `json:"fixture"`
	TimelineBase  int64      `json:"timelineBase"`
	SourceDigest  string     `json:"sourceDigest"`
	PolicyDigest  string     `json:"policyDigest"`
	TotalBytes    int64      `json:"totalBytes"`
	MinQualityPPM policy.PPM `json:"minQualityPpm"`
	Segments      []Entry    `json:"segments"`
	Digest        string     `json:"digest"`
}

// Build assembles a manifest from the split plan, the policy result and the
// source digests. Derived digests are computed from the entry's immutable
// fields, not from the source bytes, so they are reproducible in any process.
func Build(fixture string, tl *timeline.Timeline, plan *split.Plan, res *policy.Result, eventDigest string) (*Manifest, error) {
	if len(plan.Segments) != len(res.Choices) {
		return nil, model.Errorf(model.StageManifest, "build", model.TrackRef{}, 0,
			"segment count %d does not match choice count %d", len(plan.Segments), len(res.Choices))
	}
	m := &Manifest{
		Version:       1,
		Fixture:       fixture,
		TimelineBase:  tl.Base,
		SourceDigest:  tl.SourceDigest,
		PolicyDigest:  res.PolicyDigest,
		MinQualityPPM: res.MinQualityPPM,
	}
	for i, seg := range plan.Segments {
		choice := res.Choices[i]
		entry := Entry{
			Index:       seg.Index,
			StartTick:   int64(seg.Span.Start),
			EndTick:     int64(seg.Span.End),
			Bytes:       choice.Bytes,
			QualityPPM:  choice.QualityPPM,
			Level:       choice.Level,
			Triggers:    []string{seg.StartName, seg.EndName},
			SampleCount: seg.Samples,
		}
		entry.SourceDigest = model.Fingerprint(tl.SourceDigest, eventDigest, fmt.Sprint(i))
		derived, err := derivedDigest(entry)
		if err != nil {
			return nil, err
		}
		entry.DerivedDigest = derived
		if seg.TransitionIn != "" {
			entry.Transitions = append(entry.Transitions, seg.TransitionIn)
		}
		if seg.TransitionOut != "" {
			entry.Transitions = append(entry.Transitions, seg.TransitionOut)
		}
		m.Segments = append(m.Segments, entry)
	}
	for _, e := range m.Segments {
		m.TotalBytes += e.Bytes
	}
	d, err := Digest(m)
	if err != nil {
		return nil, err
	}
	m.Digest = d
	return m, nil
}

// derivedDigest hashes the fields that define a segment's derived output.
func derivedDigest(e Entry) (string, error) {
	d, err := model.DigestJSON(struct {
		Start, End int64
		Source     string
		Bytes      int64
		Quality    policy.PPM
		Level      string
		Triggers   []string
	}{e.StartTick, e.EndTick, e.SourceDigest, e.Bytes, e.QualityPPM, e.Level, e.Triggers})
	if err != nil {
		return "", model.WrapError(model.StageManifest, "digest", model.TrackRef{}, 0, "cannot digest segment", err)
	}
	return d, nil
}

// Digest computes the manifest-level digest over everything except Digest.
func Digest(m *Manifest) (string, error) {
	clone := *m
	clone.Digest = ""
	d, err := model.DigestJSON(clone)
	if err != nil {
		return "", model.WrapError(model.StageManifest, "digest", model.TrackRef{}, 0, "cannot digest manifest", err)
	}
	return d, nil
}
