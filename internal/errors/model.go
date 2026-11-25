// Package errors defines the shared anomaly/error model used across TS
// parsing, shared clock, keyframe indexing, fan-out packaging, recovery and
// the commit ledger. Every Issue carries the input interval, PID and byte
// offset it refers to so that anomaly lists are fully traceable.
package errors

import (
	"fmt"
	"sort"
	"sync"
)

// Category is the stable machine-readable class of an anomaly.
type Category string

const (
	CatSyncByte          Category = "sync_byte"
	CatTruncatedPacket   Category = "truncated_packet"
	CatCCJump            Category = "cc_jump"
	CatCCDuplicate       Category = "cc_duplicate"
	CatPSICRC            Category = "psi_crc"
	CatPSIVersionSwitch  Category = "psi_version_switch"
	CatMappingIncomplete Category = "incomplete_mapping"
	CatPCRWrap           Category = "pcr_wrap"
	CatTimeBoundary      Category = "time_boundary"
	CatKeyframeBoundary  Category = "keyframe_boundary"
	CatHashMismatch      Category = "hash_mismatch"
	CatMissingArtifact   Category = "missing_artifact"
	CatPathRejected      Category = "path_rejected"
	CatFanoutIsolated    Category = "representation_isolated"
	CatRoundtripMismatch Category = "roundtrip_mismatch"
)

// Severity levels.
const (
	SeverityWarning = "warning"
	SeverityError   = "error"
)

// RecoveryState tracks where an issue is in the recover pipeline.
type RecoveryState string

const (
	StateOpen      RecoveryState = "open"
	StatePaused    RecoveryState = "paused"
	StateRecovered RecoveryState = "recovered"
	StateIsolated  RecoveryState = "isolated"
)

// Issue is one traceable anomaly record.
type Issue struct {
	ID          string        `json:"id"`
	Category    Category      `json:"category"`
	Severity    string        `json:"severity"`
	Message     string        `json:"message"`
	InputStart  int64         `json:"input_start"`
	InputEnd    int64         `json:"input_end"`
	PID         int           `json:"pid"`
	Offset      int64         `json:"offset"`
	PacketIndex int64         `json:"packet_index"`
	IntervalID  string        `json:"interval_id,omitempty"`
	ClockTime   int64         `json:"clock_time_90k"`

	// Continuity-counter gap details.
	GapFromCC      int   `json:"gap_from_cc,omitempty"`
	GapToCC        int   `json:"gap_to_cc,omitempty"`
	GapPackets     int   `json:"gap_packets,omitempty"`
	GapPacketStart int64 `json:"gap_packet_start,omitempty"`
	GapPacketEnd   int64 `json:"gap_packet_end,omitempty"`
	GapOffsetStart int64 `json:"gap_offset_start,omitempty"`
	GapOffsetEnd   int64 `json:"gap_offset_end,omitempty"`

	// Keyframe association (byte offset of the related keyframe packet).
	RelatedKeyframeOffset int64 `json:"related_keyframe_offset,omitempty"`

	Recovery RecoveryState   `json:"recovery_state"`
	Attrs    map[string]string `json:"attrs,omitempty"`
}

// Error is a typed processing error carrying a category.
type Error struct {
	Category Category
	Offset   int64
	Message  string
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s at offset %d: %s", e.Category, e.Offset, e.Message)
}

// New builds a typed error.
func New(cat Category, offset int64, format string, args ...any) *Error {
	return &Error{Category: cat, Offset: offset, Message: fmt.Sprintf(format, args...)}
}

// Book is a concurrency-safe collector of Issues.
type Book struct {
	mu     sync.Mutex
	seq    int
	issues []Issue
}

// NewBook creates an empty issue book.
func NewBook() *Book { return &Book{} }

// Add records an issue, assigning a stable sequential ID and defaults.
func (b *Book) Add(i Issue) Issue {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	if i.ID == "" {
		i.ID = fmt.Sprintf("ISS-%04d", b.seq)
	}
	if i.Severity == "" {
		i.Severity = SeverityError
	}
	if i.Recovery == "" {
		i.Recovery = StateOpen
	}
	if i.InputEnd == 0 && i.InputStart != 0 {
		i.InputEnd = i.InputStart
	}
	b.issues = append(b.issues, i)
	return i
}

// All returns a copy of recorded issues.
func (b *Book) All() []Issue {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Issue, len(b.issues))
	copy(out, b.issues)
	return out
}

// Of returns issues of one category.
func (b *Book) Of(cat Category) []Issue {
	var out []Issue
	for _, i := range b.All() {
		if i.Category == cat {
			out = append(out, i)
		}
	}
	return out
}

// Counts returns per-category counts (safe for nil book).
func (b *Book) Counts() map[Category]int {
	counts := map[Category]int{}
	if b == nil {
		return counts
	}
	for _, i := range b.All() {
		counts[i.Category]++
	}
	return counts
}

// Mark sets the recovery state of every issue whose ID is in ids.
func (b *Book) Mark(state RecoveryState, ids ...string) int {
	if b == nil || len(ids) == 0 {
		return 0
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for k := range b.issues {
		if want[b.issues[k].ID] {
			b.issues[k].Recovery = state
			n++
		}
	}
	return n
}

// MarkCategory sets recovery state for all issues in a category.
func (b *Book) MarkCategory(cat Category, state RecoveryState) int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for k := range b.issues {
		if b.issues[k].Category == cat {
			b.issues[k].Recovery = state
			n++
		}
	}
	return n
}

// Categories returns category names sorted by name.
func Categories(m map[Category]int) []string {
	out := make([]string, 0, len(m))
	for c := range m {
		out = append(out, string(c))
	}
	sort.Strings(out)
	return out
}
