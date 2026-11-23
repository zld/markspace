// Package model defines the packet event shared by every kernel module
// and the error taxonomy used to classify malformed input.
package model

// MediaType identifies the essence carried by a packet.
type MediaType int

const (
	MediaUnknown MediaType = iota
	MediaVideo
	MediaAudio
)

func (m MediaType) String() string {
	switch m {
	case MediaVideo:
		return "video"
	case MediaAudio:
		return "audio"
	default:
		return "unknown"
	}
}

// ErrClass categorizes a malformed or rejected packet.
type ErrClass int

const (
	ErrNone              ErrClass = iota
	ErrTruncated                  // frame shorter than the parsed structure requires
	ErrBadLength                  // framing length field implausible; resync performed
	ErrBadVersion                 // RTP version field != 2
	ErrBadHeader                  // header failed validation after length checks
	ErrBadExtension               // extension header length exceeds the packet
	ErrBadPadding                 // padding length invalid for the packet size
	ErrAmbiguousRollover          // sequence number cannot be assigned to a cycle
	ErrDuplicate                  // extended sequence number already produced a sample
	ErrLate                       // arrived outside the late window
)

func (e ErrClass) String() string {
	switch e {
	case ErrNone:
		return "none"
	case ErrTruncated:
		return "truncated"
	case ErrBadLength:
		return "bad_length"
	case ErrBadVersion:
		return "bad_version"
	case ErrBadHeader:
		return "bad_header"
	case ErrBadExtension:
		return "bad_extension"
	case ErrBadPadding:
		return "bad_padding"
	case ErrAmbiguousRollover:
		return "ambiguous_rollover"
	case ErrDuplicate:
		return "duplicate"
	case ErrLate:
		return "late"
	default:
		return "unknown"
	}
}

// Kind identifies what an event carries on the unified timeline.
type Kind int

const (
	KindPacket         Kind = iota // media packet (RTP)
	KindSenderReport               // RTCP SR: cross-clock mapping
	KindReceiverReport             // RTCP RR
	KindNACK                       // RTCP feedback requesting retransmission
	KindRecovered                  // packet restored from the retransmit cache
	KindMalformed                  // unparseable bytes, Err field classifies
)

// Event is the single currency of the kernel. All times are on the
// reference clock (milliseconds): ArrivalMs is when the bytes surfaced,
// EventMs is the media presentation time derived from the RTP timestamp.
type Event struct {
	Kind       Kind
	Media      MediaType
	SSRC       uint32
	Seq        uint64 // extended sequence number (media packets)
	Seq16      uint16 // wire sequence number
	RTPTs      uint32 // RTP timestamp
	EventMs    int64  // presentation time on the reference clock
	ArrivalMs  int64  // arrival time on the reference clock
	ByteOffset int64  // offset in the input stream (parse diagnostics)
	Size       int    // packet size in bytes
	Keyframe   bool   // declared by the payload format (video)
	Err        ErrClass

	// RTCP payloads (zero for media packets).
	NTPMs    int64    // SR: NTP wall-clock in milliseconds
	NACKSeqs []uint16 // NACK: packet IDs expanded from PID+BLP
	Payload  []byte   // declared payload, nil for malformed events
}

// Summary is the deterministic result of a pipeline run. Field order is
// fixed so the canonical JSON encoding doubles as the digest input.
type Summary struct {
	Fixture             string         `json:"fixture"`
	ParsedPackets       int            `json:"parsed_packets"`
	MalformedPackets    int            `json:"malformed_packets"`
	TruncatedTail       int            `json:"truncated_tail_packets"`
	DuplicatePackets    int            `json:"duplicate_packets"`
	QuarantinedPackets  int            `json:"quarantined_packets"`
	EmittedSamples      int            `json:"emitted_samples"`
	LostPackets         int            `json:"lost_packets"`
	RecoveredPackets    int            `json:"recovered_packets"`
	RecoveryRate        float64        `json:"recovery_rate"`
	CacheMiss           int            `json:"cache_miss"`
	DupNACK             int            `json:"dup_nack"`
	LateFeedback        int            `json:"late_feedback"`
	LateReplayed        int            `json:"late_replayed"`
	LateDropped         int            `json:"late_dropped"`
	NACKReceived        int            `json:"nack_received"`
	ReceiverReports     int            `json:"receiver_reports"`
	AVSyncErrorMs       int64          `json:"av_sync_error_ms"`
	AVSyncConfidence    string         `json:"av_sync_confidence"`
	AVSyncSamples       int            `json:"av_sync_samples"`
	ClockBiasMs         int64          `json:"clock_bias_ms"`
	ClockDriftPPM       int64          `json:"clock_drift_ppm"`
	OutOfSyncSegments   int            `json:"out_of_sync_segments"`
	P95LatencyMs        int64          `json:"p95_latency_ms"`
	IndexRebuilt        int            `json:"index_rebuilt"`
	IndexComplete       bool           `json:"index_complete"`
	RecoverableSegments int            `json:"recoverable_segments"`
	UnrecoverableSegs   int            `json:"unrecoverable_segments"`
	BufferMalformed     int            `json:"buffer_malformed"`
	SyncMalformed       int            `json:"sync_malformed"`
	MalformedByClass    map[string]int `json:"malformed_by_class"`
	QuarantineReasons   map[string]int `json:"quarantine_reasons"`
	ResultSHA256        string         `json:"result_sha256"`
}
