package pipeline

import (
	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/manifest"
)

func buildManifest(st *State, issues []*fault.Error) *manifest.Manifest {
	m := &manifest.Manifest{
		TaskKey:    st.TaskKey,
		Scope:      "media-check-demo",
		Corruption: st.Config.Corrupt,
		Inputs:     []manifest.InputRecord{st.Input},
		Issues:     issues,
		Stages:     append([]string(nil), st.Stages...),
		Checkpoint: manifest.CheckpointInfo{
			LastStage:       lastStage(st.Stages),
			CompletedStages: append([]string(nil), st.Stages...),
		},
	}
	if st.Timeline != nil {
		m.Tracks = st.Timeline.Tracks
		m.DuplicateGroups = st.Timeline.DuplicateGroups
		m.Summary.Tracks = len(st.Timeline.Tracks)
		m.Summary.DuplicateGroups = len(st.Timeline.DuplicateGroups)
		m.Summary.AlignmentErrorMS = st.Timeline.Alignment.MaxErrorMS
	}
	if st.Diagnostic != nil {
		m.Diagnostics = st.Diagnostic
	}
	if st.Export != nil {
		m.Export = st.Export
		m.Summary.Frames = len(st.Export.Frames)
		m.Summary.PixelMin = st.Export.Pixels.Min
		m.Summary.PixelMax = st.Export.Pixels.Max
		m.Summary.PixelMean = st.Export.Pixels.Mean
		m.Summary.PeakDBFS = st.Export.Audio.PeakDBFS
		m.Summary.FrameSHA256 = st.Export.FrameSHA256
		m.Summary.SegmentSHA256 = st.Export.SegmentSHA256
		m.Summary.AlignmentErrorMS = st.Export.AlignmentErrorMS
		m.FingerprintGroups = manifest.BuildFingerprintGroups(st.Export.Segments)
	}
	return m
}

func lastStage(stages []string) string {
	if len(stages) == 0 {
		return ""
	}
	return stages[len(stages)-1]
}
