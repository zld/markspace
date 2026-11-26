package media

// Block is one decoded Matroska/SimpleBlock or Block payload.
type Block struct {
	ElementOffset   int64  `json:"element_offset"`
	DataOffset      int64  `json:"data_offset"`
	Path            string `json:"path"`
	TrackID         uint32 `json:"track_id"`
	Sequence        int    `json:"sequence"`
	GlobalSequence  int    `json:"global_sequence"`
	ClusterTC       int64  `json:"cluster_timecode"`
	RelativeTC      int16  `json:"relative_timecode"`
	TimecodeScaleNS uint64 `json:"timecode_scale_ns"`
	RawPTSNS        int64  `json:"raw_pts_ns"`
	PTSNS           int64  `json:"pts_ns"`
	DurationNS      int64  `json:"duration_ns"`
	Keyframe        bool   `json:"keyframe"`
	SourceID        string `json:"source_id,omitempty"`
	Payload         []byte `json:"-"`
}
