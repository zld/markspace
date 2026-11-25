package diagnostic

import (
	"errors"
	"fmt"
)

// Location is the shared, stable error position model used by every stage.
type Location struct {
	Stage          string `json:"stage"`
	ManifestPath   string `json:"manifest_path,omitempty"`
	Representation string `json:"representation,omitempty"`
	EntryKey       string `json:"entry_key,omitempty"`
	EntryIndex     int    `json:"entry_index"`
	Sequence       int    `json:"sequence,omitempty"`
	ByteOffset     int64  `json:"byte_offset"`
	Field          string `json:"field,omitempty"`
}

func (l Location) String() string {
	s := l.Stage
	if l.ManifestPath != "" {
		s += " manifest=" + l.ManifestPath
	}
	if l.Representation != "" {
		s += " representation=" + l.Representation
	}
	if l.EntryKey != "" {
		s += " entry=" + l.EntryKey
	}
	if l.EntryIndex >= 0 {
		s += fmt.Sprintf(" entry_index=%d", l.EntryIndex)
	}
	if l.Sequence != 0 {
		s += fmt.Sprintf(" sequence=%d", l.Sequence)
	}
	if l.ByteOffset >= 0 {
		s += fmt.Sprintf(" byte_offset=%d", l.ByteOffset)
	}
	if l.Field != "" {
		s += " field=" + l.Field
	}
	return s
}

// Error carries a location and an optional machine-readable kind.
type Error struct {
	Kind error
	Loc  Location
	Err  error
}

func (e *Error) Error() string {
	msg := e.Loc.String()
	if e.Kind != nil {
		msg += " kind=" + e.Kind.Error()
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() []error {
	if e.Kind != nil && e.Err != nil {
		return []error{e.Kind, e.Err}
	}
	if e.Kind != nil {
		return []error{e.Kind}
	}
	if e.Err != nil {
		return []error{e.Err}
	}
	return nil
}

func Wrap(kind error, loc Location, detail any) *Error {
	var err error
	switch v := detail.(type) {
	case nil:
		err = nil
	case error:
		err = v
	case string:
		err = errors.New(v)
	default:
		err = fmt.Errorf("%v", v)
	}
	return &Error{Kind: kind, Loc: loc, Err: err}
}

func New(kind error, loc Location, msg string) *Error {
	return Wrap(kind, loc, errors.New(msg))
}

// As extracts the first located error in err's tree.
func As(err error) (*Error, bool) {
	var le *Error
	if errors.As(err, &le) {
		return le, true
	}
	return nil, false
}

// Position is a compact failure report used by the CLI and verify output.
type Position struct {
	Stage          string `json:"stage"`
	ManifestPath   string `json:"manifest_path,omitempty"`
	Representation string `json:"representation,omitempty"`
	EntryKey       string `json:"entry_key,omitempty"`
	EntryIndex     int    `json:"entry_index,omitempty"`
	Sequence       int    `json:"sequence,omitempty"`
	ByteOffset     int64  `json:"byte_offset"`
	Kind           string `json:"kind,omitempty"`
	Message        string `json:"message"`
}

func PositionOf(err error) Position {
	le, ok := As(err)
	if !ok {
		return Position{ByteOffset: -1, Message: err.Error()}
	}
	p := Position{
		Stage:          le.Loc.Stage,
		ManifestPath:   le.Loc.ManifestPath,
		Representation: le.Loc.Representation,
		EntryKey:       le.Loc.EntryKey,
		EntryIndex:     le.Loc.EntryIndex,
		Sequence:       le.Loc.Sequence,
		ByteOffset:     le.Loc.ByteOffset,
		Message:        le.Error(),
	}
	if le.Kind != nil {
		p.Kind = le.Kind.Error()
	}
	return p
}
