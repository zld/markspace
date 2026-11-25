package media

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// Sentinel errors shared by every subsystem so callers can classify failures
// with errors.Is instead of matching strings.
var (
	// ErrFrameValidation is the base for any video frame metadata/hash fault.
	ErrFrameValidation = errors.New("frame validation failed")
	// ErrRTPLoss is returned when audio loss cannot be recovered.
	ErrRTPLoss = errors.New("unrecoverable RTP packet loss")
	// ErrRTPOrder marks a reorder window overflow.
	ErrRTPOrder = errors.New("RTP reorder window overflow")
	// ErrLimitExceeded is returned when input byte or packet caps are hit.
	ErrLimitExceeded = errors.New("input limit exceeded")
	// ErrCheckpoint is returned when a checkpoint is missing or unusable.
	ErrCheckpoint = errors.New("invalid checkpoint")
	// ErrDigestMismatch means a published artifact does not match its record.
	ErrDigestMismatch = errors.New("artifact digest mismatch")
	// ErrManifestConflict means two records share a key with different digests.
	ErrManifestConflict = errors.New("manifest conflict")
	// ErrPathEscape rejects output paths escaping the configured workspace.
	ErrPathEscape = errors.New("path escapes workspace root")
)

// Frame is one generated still image frame with its full provenance metadata.
type Frame struct {
	// Index is the strict monotonic position in the sequence (0 based).
	Index int `json:"index"`
	// Seq is the declared sequence number; fixtures may perturb it.
	Seq int `json:"seq"`
	// TimestampTicks is the presentation time on the video timescale.
	TimestampTicks int64 `json:"timestamp_ticks"`
	// Width and Height describe the generated raster.
	Width  int `json:"width"`
	Height int `json:"height"`
	// Payload is the synthetic, deterministically generated image bytes.
	Payload []byte `json:"-"`
	// DeclaredSHA256 is what the producer claimed; validation recomputes it.
	DeclaredSHA256 string `json:"declared_sha256"`
}

// ComputeSHA256 returns the lowercase hex digest of the frame payload.
func ComputeSHA256(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// VerifyHash recomputes the payload digest and compares it with the declared one.
func (f Frame) VerifyHash() bool {
	return f.DeclaredSHA256 == ComputeSHA256(f.Payload)
}

// FrameFault records a single validation failure against a frame index.
type FrameFault struct {
	Index  int    `json:"index"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

func (f FrameFault) Error() string {
	return fmt.Sprintf("frame %d: %s: %s", f.Index, f.Kind, f.Detail)
}

// FrameCounters aggregates the three ordered-sequence fault classes.
type FrameCounters struct {
	Duplicate  int `json:"duplicate"`
	Missing    int `json:"missing"`
	OutOfRange int `json:"missing_out_of_range"`
	BadHash    int `json:"bad_hash"`
}

// FrameReport is the structured output of frame validation.
type FrameReport struct {
	Accepted int           `json:"accepted"`
	Counters FrameCounters `json:"counters"`
	Faults   []FrameFault  `json:"faults"`
}

// Failed reports whether any validation fault makes the task fail.
func (r FrameReport) Failed() bool {
	return r.Counters.Duplicate+r.Counters.Missing+r.Counters.OutOfRange+
		r.Counters.BadHash > 0 || len(r.Faults) > 0
}

// AudioPacket is one synthetic RTP audio packet plus its loss bookkeeping.
type AudioPacket struct {
	// Seq is the RTP sequence number (uint16 domain).
	Seq uint16 `json:"seq"`
	// RTPTimestamp is the RTP header timestamp on the audio clock.
	RTPTimestamp uint32 `json:"rtp_timestamp"`
	// DurationTicks is the payload duration on the same audio clock.
	DurationTicks uint32 `json:"duration_ticks"`
	Payload       []byte `json:"-"`
	// InjectedLost marks packets removed by the fixture before delivery.
	InjectedLost bool `json:"injected_lost,omitempty"`
	// Recovered records successful PLC/loss repair.
	Recovered bool `json:"recovered,omitempty"`
	// Unrecoverable marks loss the configured policy cannot repair.
	Unrecoverable bool `json:"unrecoverable,omitempty"`
}

// LossReport quantifies injected audio loss and its recovery outcome.
type LossReport struct {
	Injected      int `json:"injected"`
	Recovered     int `json:"recovered"`
	Unrecoverable int `json:"unrecoverable"`
	// RecoveryRate is Recovered/Injected; 1 when no loss was injected.
	RecoveryRate float64 `json:"recovery_rate"`
}

// SyncSample is one audio/video alignment observation on the shared clock.
type SyncSample struct {
	FrameIndex int   `json:"frame_index"`
	VideoNanos int64 `json:"video_nanos"`
	AudioNanos int64 `json:"audio_nanos"`
	// ErrorNanos is audio minus video presentation time.
	ErrorNanos int64 `json:"error_nanos"`
}

// SyncReport is the quantifiable A/V synchronization result.
type SyncReport struct {
	Samples       []SyncSample `json:"samples"`
	SampleCount   int          `json:"sample_count"`
	MaxAbsErrorNs int64        `json:"max_abs_error_nanos"`
	MeanErrorNs   int64        `json:"mean_error_nanos"`
}

// SyncErrorMs exposes the worst absolute synchronization error in ms.
func (s SyncReport) SyncErrorMs() float64 { return NanosToMs(s.MaxAbsErrorNs) }
