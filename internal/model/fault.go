package model

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Exit codes fixed by the terminal acceptance contract.
const (
	ExitOK           = 0
	ExitCorruptInput = 2
	ExitSyncConfig   = 3
	ExitInterrupted  = 10
)

// Kind is the stable machine-readable fault category.
type Kind string

const (
	FaultBadSHA            Kind = "bad_sha"
	FaultTruncated         Kind = "truncated"
	FaultBadTimestamp      Kind = "bad_timestamp"
	FaultBadLength         Kind = "bad_length"
	FaultBadMagic          Kind = "bad_magic"
	FaultDuplicateFrame    Kind = "duplicate_frame"
	FaultDuplicatePacket   Kind = "duplicate_packet"
	FaultLatePacket        Kind = "late_packet"
	FaultOutOfOrder        Kind = "out_of_order"
	FaultRTPLoss           Kind = "rtp_loss"
	FaultWindowClosed      Kind = "window_closed"
	FaultSync              Kind = "sync_error"
	FaultUnknownChannel    Kind = "unknown_channel_layout"
	FaultUnsupportedRate   Kind = "unsupported_sample_rate"
	FaultColorConflict     Kind = "color_metadata_conflict"
	FaultCheckpointCorrupt Kind = "checkpoint_corrupt"
	FaultInputMismatch     Kind = "input_digest_mismatch"
	FaultChunkHash         Kind = "chunk_hash_mismatch"
	FaultInterrupted       Kind = "interrupted"
)

// Fault is the common error/location/recovery record. Offset is the exact
// byte associated with the damage when the container format can determine it.
type Fault struct {
	Kind        Kind   `json:"kind"`
	MaterialID  string `json:"material_id,omitempty"`
	RecordIndex int64  `json:"record_index"`
	Offset      int64  `json:"offset"`
	RecordStart int64  `json:"record_start,omitempty"`
	Source      string `json:"source,omitempty"`
	Message     string `json:"message,omitempty"`
	Recoverable bool   `json:"recoverable"`
	Recovered   bool   `json:"recovered"`
	Expected    string `json:"expected,omitempty"`
	Actual      string `json:"actual,omitempty"`
}

// ExitError associates an operational exit code with faults.
type ExitError struct {
	Code   int
	Err    error
	Faults []Fault
}

func (e *ExitError) Error() string {
	if e == nil || e.Err == nil {
		return fmt.Sprintf("mediarc exit %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

// NewExitError constructs an exit error.
func NewExitError(code int, msg string, faults ...Fault) *ExitError {
	return &ExitError{Code: code, Err: errors.New(msg), Faults: faults}
}

// ExitCode extracts the declared exit code, defaulting to 1 for ordinary
// errors and 0 for nil.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return 1
}

// Collector is a concurrency-safe fault sink.
type Collector struct {
	mu     sync.Mutex
	faults []Fault
}

// Add records one fault.
func (c *Collector) Add(f Fault) Fault {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.faults = append(c.faults, f)
	return f
}

// All returns a sorted copy for deterministic reports.
func (c *Collector) All() []Fault {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]Fault(nil), c.faults...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Offset != out[j].Offset {
			return out[i].Offset < out[j].Offset
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].RecordIndex < out[j].RecordIndex
	})
	return out
}

// Count returns occurrences of a kind.
func (c *Collector) Count(kinds ...Kind) int {
	set := make(map[Kind]struct{}, len(kinds))
	for _, k := range kinds {
		set[k] = struct{}{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, f := range c.faults {
		if len(set) == 0 {
			n++
			continue
		}
		if _, ok := set[f.Kind]; ok {
			n++
		}
	}
	return n
}

// Has reports whether any fault has one of kinds.
func (c *Collector) Has(kinds ...Kind) bool { return c.Count(kinds...) > 0 }
