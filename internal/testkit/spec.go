// Package testkit builds deterministic fixtures. This file declares the
// document and track specification types.
package testkit

import (
	"mediad/internal/ebml"
)

// TrackSpec describes one generated track.
type TrackSpec struct {
	Number        uint64
	Type          ebml.ID // use TrackTypeVideo/TrackTypeAudio
	Codec         string
	Name          string
	Language      string
	FrameDurMs    int // container units (milliseconds at the default scale)
	FrameBytes    int // payload size per block
	KeyEvery      int // every N-th video block is a keyframe; ignored for audio
	StartOffsetMs uint32
	DriftPPM      int // device clock error in parts per million
	// SparseEvery > 0 drops that fraction's audio blocks (1 in N) to model a
	// sparse audio track.
	SparseEvery int
}

// Synthetic TrackType ids.
var (
	TrackTypeVideo = ebml.IDTrackType
	TrackTypeAudio = ebml.IDTrackType
)

// DocSpec describes a whole synthetic document.
type DocSpec struct {
	DocType          string
	ScaleNs          int64
	SegmentUnknown   bool
	ClusterUnknown   bool
	Tracks           []TrackSpec
	ClipDurMs        int
	BlocksPerCluster int
	Seed             int64
}
