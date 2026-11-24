package model

import "fmt"

// ErrCode is a stable machine-readable error code. Codes are recorded in the
// scan index and in checkpoints so a rerun can be audited without re-parsing
// human-readable messages.
type ErrCode string

const (
	ErrTruncated          ErrCode = "TRUNCATED"           // file/box ends before declared size
	ErrCorruptBox         ErrCode = "CORRUPT_BOX"         // invalid box/KLV header
	ErrEssenceCRC         ErrCode = "ESSENCE_CRC"         // essence packet failed integrity check
	ErrIndexMissing       ErrCode = "INDEX_MISSING"       // no usable index, caller must scan
	ErrIndexIncomplete    ErrCode = "INDEX_INCOMPLETE"    // index file damaged, caller must scan
	ErrIndexStale         ErrCode = "INDEX_STALE"         // input digest changed, index rebuilt
	ErrCheckpointMismatch ErrCode = "CHECKPOINT_MISMATCH"  // input digest differs from checkpoint
	ErrAVDesync           ErrCode = "AV_DESYNC"           // cross-track deviation over tolerance
	ErrPacketLost         ErrCode = "PACKET_LOST"         // RTP sequence gap expired the window
	ErrTimestampJump      ErrCode = "TIMESTAMP_JUMP"      // RTP timestamp discontinuity
	ErrInterrupted        ErrCode = "INTERRUPTED"         // run stopped at a packet boundary
	ErrVerify             ErrCode = "VERIFY_FAILED"       // output checksum mismatch
)

// ScanError pinpoints one defect: where it is (byte offset), which track it
// belongs to (-1 = container level) and why.
type ScanError struct {
	Code   ErrCode `json:"code"`
	Offset int64   `json:"offset"`
	Track  int     `json:"track"`
	Detail string  `json:"detail"`
}

func (e ScanError) Error() string {
	return fmt.Sprintf("%s @%d track=%d: %s", e.Code, e.Offset, e.Track, e.Detail)
}

// Errors is an ordered list of defects with small query helpers.
type Errors []ScanError

// Has reports whether any error carries the given code.
func (es Errors) Has(code ErrCode) bool {
	for _, e := range es {
		if e.Code == code {
			return true
		}
	}
	return false
}

// Count returns how many errors carry the given code.
func (es Errors) Count(code ErrCode) int {
	n := 0
	for _, e := range es {
		if e.Code == code {
			n++
		}
	}
	return n
}

// Damaged reports whether the input itself (not the derived state) is broken.
func (es Errors) Damaged() bool {
	return es.Has(ErrTruncated) || es.Has(ErrCorruptBox) || es.Has(ErrEssenceCRC)
}
