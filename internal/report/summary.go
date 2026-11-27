// Package report aggregates the evidence produced by mapping, timeline and
// remux into one deterministic, JSON-serializable summary. All numeric
// aggregates are computed with integers; the SHA-256 covers the exact output
// bytes so identical reruns yield identical summaries.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sort"

	"mediapack/internal/maptrack"
	"mediapack/internal/model"
	"mediapack/internal/remux"
	"mediapack/internal/timeline"
)

// SampleLine is one printed source-to-target mapping row.
type SampleLine struct {
	Source       string `json:"source"`
	SourceTrack  uint32 `json:"sourceTrack"`
	TargetTrack  uint32 `json:"targetTrack"`
	Index        uint32 `json:"index"`
	SrcOffset    int64  `json:"srcOffset"`
	TargetOffset int64  `json:"targetOffset"`
	Size         uint32 `json:"size"`
}

// Failure captures the first locatable error for the report.
type Failure struct {
	Code     int    `json:"code"`
	Message  string `json:"message"`
	File     string `json:"file,omitempty"`
	Offset   int64  `json:"offset"`
	Track    uint32 `json:"track,omitempty"`
	TimeTick int64  `json:"timeTick,omitempty"`
	EventSeq uint64 `json:"eventSeq,omitempty"`
}

// Summary is the full printed evidence block. Field order is fixed so JSON
// output is stable across runs.
type Summary struct {
	Status            string              `json:"status"`
	TrackCount        int                 `json:"trackCount"`
	SampleCount       int                 `json:"sampleCount"`
	MaxTimeDeltaTick  int64               `json:"maxTimeDeltaTick"`
	ByteDiff          int64               `json:"byteDiff"`
	ReplayCount       int                 `json:"replayCount"`
	WrapCount         int                 `json:"wrapCount"`
	OverlapCount      int                 `json:"overlapCount"`
	GapCount          int                 `json:"gapCount"`
	OutputSHA256      string              `json:"outputSha256"`
	Samples           []SampleLine        `json:"samples"`
	Boundaries        []model.Boundary    `json:"boundaries"`
	MetadataDecisions []maptrack.Decision `json:"metadataDecisions"`
	Failure           *Failure            `json:"failure,omitempty"`
}

// Inputs bundles the subsystem outputs the summary is built from.
type Inputs struct {
	Mapping    *maptrack.Mapping
	Timeline   *timeline.Timeline
	Manifest   *remux.Manifest
	OutputPath string
	Err        error
}

// Build constructs a deterministic summary. It never panics on a nil optional
// input; absent subsystems simply contribute zero/empty values.
func Build(in Inputs) *Summary {
	s := &Summary{Status: "ok", Samples: []SampleLine{}, Boundaries: []model.Boundary{}}

	if in.Mapping != nil {
		s.TrackCount = len(in.Mapping.Bindings)
		s.appendMapping(in.Mapping)
	}
	if in.Timeline != nil {
		bounds := in.Timeline.Boundaries()
		s.Boundaries = bounds
		s.MaxTimeDeltaTick = maxAbsDelta(bounds)
		s.ReplayCount = in.Timeline.ReplayCount
		s.WrapCount = in.Timeline.WrapCount
		s.OverlapCount = in.Timeline.OverlapCount
		s.GapCount = in.Timeline.GapCount
	}
	if in.Manifest != nil {
		s.ByteDiff = in.Manifest.ByteDiff
		s.SampleCount = in.Manifest.SampleCount
	}
	if in.OutputPath != "" {
		s.OutputSHA256 = hashFile(in.OutputPath)
	}
	if in.Err != nil {
		s.Status = "error"
		s.Failure = toFailure(in.Err)
	}
	return s
}

func (s *Summary) appendMapping(m *maptrack.Mapping) {
	for _, b := range m.Bindings {
		s.MetadataDecisions = append(s.MetadataDecisions, b.Decisions...)
		for _, rec := range b.Samples {
			s.Samples = append(s.Samples, SampleLine{
				Source:      rec.Source,
				SourceTrack: rec.SourceTrack,
				TargetTrack: b.TargetTrack,
				Index:       rec.Index,
				SrcOffset:   rec.SrcOffset,
				Size:        rec.Size,
			})
		}
	}
	// Deterministic sample ordering even if bindings/samples were appended in
	// different orders across runs.
	sort.SliceStable(s.Samples, func(i, j int) bool {
		a, b := s.Samples[i], s.Samples[j]
		if a.TargetTrack != b.TargetTrack {
			return a.TargetTrack < b.TargetTrack
		}
		return a.Index < b.Index
	})
}

// AttachManifestOffsets fills target offsets from the committed manifest into
// the sample lines, keyed by (target track, index).
func (s *Summary) AttachManifestOffsets(mf *remux.Manifest) {
	type key struct {
		track uint32
		index uint32
	}
	off := map[key]int64{}
	for _, r := range mf.Records {
		off[key{r.TargetTrack, r.Index}] = r.TargetOffset
	}
	for i := range s.Samples {
		s.Samples[i].TargetOffset = off[key{s.Samples[i].TargetTrack, s.Samples[i].Index}]
	}
}

func maxAbsDelta(bounds []model.Boundary) int64 {
	var max int64
	for _, b := range bounds {
		d := b.Delta
		if d < 0 {
			d = -d
		}
		if d > max {
			max = d
		}
	}
	return max
}

func hashFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func toFailure(err error) *Failure {
	me, ok := model.AsMediaError(err)
	if !ok {
		return &Failure{
			Code:    int(model.CodeMalformedContainer),
			Message: err.Error(), Offset: -1,
		}
	}
	return &Failure{
		Code: int(me.Code), Message: me.Message, File: me.File,
		Offset: me.Offset, Track: me.Track,
		TimeTick: me.TimeTick, EventSeq: me.EventSeq,
	}
}
