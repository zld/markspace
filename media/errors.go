package media

import "fmt"

// Kind classifies a locatable error so callers can map it to an exit code.
type Kind int

const (
	KindUnknown Kind = iota
	// KindBadInput: malformed/truncated input, parameter errors.
	KindBadInput
	// KindUnsupported: rate/container combination outside the sample scope.
	KindUnsupported
	// KindConflict: metadata conflict that cannot be adjudicated.
	KindConflict
	// KindUnrecoverable: corruption beyond the recovery window.
	KindUnrecoverable
)

// LocatableError carries a machine-readable code, the logical component that
// raised it and a zero-based byte/record offset into the offending input.
type LocatableError struct {
	Kind   Kind
	Code   string
	Module string
	Offset int64 // byte offset for containers, record index for RTP logs; -1 if N/A
	Detail string
}

// Error implements error.
func (e *LocatableError) Error() string {
	if e.Offset >= 0 {
		return fmt.Sprintf("%s: %s at %s offset %d: %s", e.Module, e.Code, locUnit(e.Module), e.Offset, e.Detail)
	}
	return fmt.Sprintf("%s: %s: %s", e.Module, e.Code, e.Detail)
}

func locUnit(module string) string {
	if module == "rtp" {
		return "record"
	}
	return "byte"
}

// NewError builds a LocatableError with an explicit offset.
func NewError(kind Kind, code, module string, offset int64, format string, args ...any) *LocatableError {
	return &LocatableError{
		Kind:   kind,
		Code:   code,
		Module: module,
		Offset: offset,
		Detail: fmt.Sprintf(format, args...),
	}
}

// AsLocatable extracts a *LocatableError from err, if present.
func AsLocatable(err error) (*LocatableError, bool) {
	if err == nil {
		return nil, false
	}
	if le, ok := err.(*LocatableError); ok {
		return le, true
	}
	return nil, false
}

// MustLocatable fails t if err is not a LocatableError and returns it.
// Test helper shared across packages.
func MustLocatable(t interface{ Fatalf(string, ...any) }, err error) *LocatableError {
	le, ok := AsLocatable(err)
	if !ok {
		t.Fatalf("want LocatableError, got %T: %v", err, err)
	}
	return le
}
