package model

// RawEvent is one timestamped sample as parsed from the container.
//
// RawTimestamp is intentionally preserved before wraparound expansion or
// drift correction. Payload is not serialized into JSON/checkpoints.
type RawEvent struct {
	TrackID        uint32 `json:"track_id"`
	Representation string `json:"representation"`
	BitrateKbps    int    `json:"bitrate_kbps,omitempty"`
	SampleNumber   uint32 `json:"sample_number"`
	ArrivalIndex   int    `json:"arrival_index"`
	OrderIndex     int    `json:"order_index"`

	RawTimestamp  uint64 `json:"raw_timestamp"`
	TimestampBits int    `json:"timestamp_bits"`
	Timescale     uint32 `json:"timescale"`
	RawNS         NS     `json:"raw_ns"`
	DurationNS    NS     `json:"duration_ns,omitempty"`

	Keyframe bool `json:"keyframe"`

	BoxOffset  int64 `json:"box_offset"`
	DataOffset int64 `json:"data_offset,omitempty"`
	DataSize   int   `json:"data_size,omitempty"`

	Payload []byte `json:"-"`
}

// TrackMetadata describes one parsed MP4/MOV track.
type TrackMetadata struct {
	TrackID        uint32 `json:"track_id"`
	Representation string `json:"representation"`
	HandlerType    string `json:"handler_type"`
	BitrateKbps    int    `json:"bitrate_kbps,omitempty"`
	Timescale      uint32 `json:"timescale"`
	MdhdOffset     int64  `json:"mdhd_offset"`
	EventCount     int    `json:"event_count"`
	Empty          bool   `json:"empty"`
}

// Issue is a non-fatal but explicit processing result.
type Issue struct {
	Code    string `json:"code"`
	TrackID uint32 `json:"track_id,omitempty"`
	Segment int    `json:"segment,omitempty"`
	Message string `json:"message"`
	Count   int    `json:"count,omitempty"`
	Offset  int64  `json:"byte_offset,omitempty"`
}
