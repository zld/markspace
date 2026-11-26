package model

import "fmt"

// AnomalyType categorizes a timed-metadata anomaly. Types are part of the
// manifest contract and must stay stable across releases.
type AnomalyType string

const (
	AnomalyChecksum          AnomalyType = "CHECKSUM_MISMATCH" // segment hash did not verify
	AnomalyTimeWrap          AnomalyType = "TIMEBASE_WRAP"     // timestamps moved backwards
	AnomalyDuplicate         AnomalyType = "DUPLICATE_EVENT"   // same event seen twice
	AnomalyOutOfOrder        AnomalyType = "OUT_OF_ORDER"      // later event arrived earlier
	AnomalyUndecidable       AnomalyType = "UNDECIDABLE"       // ordering cannot be determined
	AnomalyUnparseable       AnomalyType = "UNPARSEABLE"       // box/packet could not be parsed
	AnomalySourceChanged     AnomalyType = "SOURCE_CHANGED"    // source replaced mid-read
	AnomalyUnrecoverable     AnomalyType = "UNRECOVERABLE"     // source itself is corrupt
	AnomalyGap               AnomalyType = "GAP"               // missing segment in a track
	AnomalyTimestampOverflow AnomalyType = "TS_OVERFLOW"       // conversion overflowed
)

// RecoveryState is the lifecycle of an anomaly inside the recover subsystem.
type RecoveryState string

const (
	StateOpen          RecoveryState = "OPEN"          // detected, not yet processed
	StateRecovered     RecoveryState = "RECOVERED"     // isolated segment recomputed
	StateUnrecoverable RecoveryState = "UNRECOVERABLE" // source damage, cannot recompute
)

// Exit codes used by the mediad command. They are declared here so tests and
// the CLI share one source of truth.
const (
	ExitOK            = 0 // all anomalies recovered (or none)
	ExitUsage         = 1 // bad arguments / execution error
	ExitUnrecoverable = 2 // manifest retained, at least one unrecoverable range
)

// Anomaly is one entry of the exception manifest. Duplicate, out-of-order and
// undecidable events keep their track, source offset and original time — they
// are never silently "fixed" by the pipeline.
type Anomaly struct {
	ID           string        `json:"id"`
	Type         AnomalyType   `json:"type"`
	TrackID      uint32        `json:"track_id"`
	TrackKind    string        `json:"track_kind"`
	StartNanos   int64         `json:"start_nanos"`
	EndNanos     int64         `json:"end_nanos"`
	SourceOffset int64         `json:"source_offset"` // byte offset inside the source
	SourceLength int64         `json:"source_length"` // affected byte range length
	TimeBase     Rational      `json:"time_base"`
	RawTick      int64         `json:"raw_tick"`      // original timestamp before conversion
	Checksum     string        `json:"checksum"`      // observed checksum (hex), may be empty
	ExpectedHash string        `json:"expected_hash"` // expected checksum when known
	Message      string        `json:"message"`
	Recovery     RecoveryState `json:"recovery_state"`
}

// StableKey orders anomalies deterministically and identifies them between runs
// so repeated executions produce byte-identical manifests.
func (a Anomaly) StableKey() string {
	return fmt.Sprintf("%d|%d|%d|%s|%s", a.TrackID, a.StartNanos, a.SourceOffset, a.Type, a.ID)
}
