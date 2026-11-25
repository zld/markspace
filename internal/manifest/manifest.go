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

// ClipRef binds a derived artifact to the segment that produced it. Its digest
// is the content address of the artifact bytes, so verification can recompute
// it from storage rather than trusting the manifest.
type ClipRef struct {
	Segment    int
	Digest     string
	Bytes      int64
	Level      string
	QualityPPM policy.PPM
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

// Build assembles a manifest from the split plan, the policy result, the
// derived clip addresses and the source digests.
func Build(fixture string, tl *timeline.Timeline, plan *split.Plan, res *policy.Result, clips []ClipRef, eventDigest string) (*Manifest, error) {
	if len(plan.Segments) != len(res.Choices) || len(clips) != len(plan.Segments) {
		return nil, model.Errorf(model.StageManifest, "build", model.TrackRef{}, 0,
			"segment count %d, choice count %d and clip count %d disagree", len(plan.Segments), len(res.Choices), len(clips))
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
		clip := clips[i]
		if clip.Segment != seg.Index || !model.ValidDigest(clip.Digest) {
			return nil, model.Errorf(model.StageManifest, "build", model.TrackRef{}, 0,
				"clip for segment %d has invalid address", seg.Index)
		}
		entry := Entry{
			Index:         seg.Index,
			StartTick:     int64(seg.Span.Start),
			EndTick:       int64(seg.Span.End),
			SourceDigest:  model.Fingerprint(tl.SourceDigest, eventDigest, fmt.Sprint(seg.Index)),
			DerivedDigest: clip.Digest,
			Bytes:         clip.Bytes,
			QualityPPM:    choice.QualityPPM,
			Level:         choice.Level,
			Triggers:      []string{seg.StartName, seg.EndName},
			SampleCount:   seg.Samples,
		}
		if seg.TransitionIn != "" {
			entry.Transitions = append(entry.Transitions, seg.TransitionIn)
		}
		if seg.TransitionOut != "" {
			entry.Transitions = append(entry.Transitions, seg.TransitionOut)
		}
		m.Segments = append(m.Segments, entry)
		m.TotalBytes += entry.Bytes
	}
	d, err := Digest(m)
	if err != nil {
		return nil, err
	}
	m.Digest = d
	return m, nil
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
