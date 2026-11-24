package model

import "fmt"

// RecoveryLevel classifies how the pipeline treated a problem.
type RecoveryLevel int

const (
	// LevelInfo is informational; processing was unaffected.
	LevelInfo RecoveryLevel = iota
	// LevelRecovered means a deterministic local repair was applied.
	LevelRecovered
	// LevelIsolated means the affected track was quarantined; unrelated
	// tracks continue to be processed.
	LevelIsolated
	// LevelFatal means the input cannot be processed at all.
	LevelFatal
)

func (l RecoveryLevel) String() string {
	switch l {
	case LevelInfo:
		return "info"
	case LevelRecovered:
		return "recovered"
	case LevelIsolated:
		return "isolated"
	case LevelFatal:
		return "fatal"
	}
	return "unknown"
}

// MarshalJSON renders the level as its stable string form.
func (l RecoveryLevel) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%q", l.String())), nil
}

// Error is the shared error model: every diagnostic carries the track, the
// byte offset into the original input, and the recovery level applied.
type Error struct {
	Level      RecoveryLevel `json:"level"`
	Op         string        `json:"op"`
	TrackID    uint32        `json:"track_id,omitempty"`
	SourceFile string        `json:"source_file,omitempty"`
	ByteOffset int64         `json:"byte_offset"` // -1 when not locatable
	Msg        string        `json:"msg"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	loc := ""
	if e.ByteOffset >= 0 {
		loc = fmt.Sprintf(" @%s:%d", e.SourceFile, e.ByteOffset)
	}
	return fmt.Sprintf("[%s] %s track=%d%s: %s", e.Level, e.Op, e.TrackID, loc, e.Msg)
}

// NewError builds an Error with an explicit byte offset.
func NewError(level RecoveryLevel, op, file string, offset int64, track uint32, format string, args ...any) *Error {
	return &Error{
		Level:      level,
		Op:         op,
		TrackID:    track,
		SourceFile: file,
		ByteOffset: offset,
		Msg:        fmt.Sprintf(format, args...),
	}
}

// Errors is an ordered diagnostics list. It is kept deterministic: entries
// are appended in processing order and never reordered by maps.
type Errors struct {
	list []*Error
}

// Add appends a diagnostic.
func (es *Errors) Add(e *Error) { es.list = append(es.list, e) }

// All returns the diagnostics in append order.
func (es *Errors) All() []*Error { return es.list }

// MaxLevel returns the highest recovery level seen, or LevelInfo when empty.
func (es *Errors) MaxLevel() RecoveryLevel {
	max := LevelInfo
	for _, e := range es.list {
		if e.Level > max {
			max = e.Level
		}
	}
	return max
}

// CountAt returns how many diagnostics sit at or above the given level.
func (es *Errors) CountAt(level RecoveryLevel) int {
	n := 0
	for _, e := range es.list {
		if e.Level >= level {
			n++
		}
	}
	return n
}
