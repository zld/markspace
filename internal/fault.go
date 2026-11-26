package internal

import (
	"errors"
	"fmt"
)

// Fault is an error with the processing phase, source identity and byte/line
// offset where the problem was observed.
type Fault struct {
	Stage  string `json:"stage"`
	Source string `json:"source,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Err    error  `json:"-"`
}

func NewFault(stage, source string, offset int, format string, args ...any) *Fault {
	return &Fault{
		Stage:  stage,
		Source: source,
		Offset: offset,
		Err:    fmt.Errorf(format, args...),
	}
}

func WrapFault(stage, source string, offset int, err error) *Fault {
	if err == nil {
		return nil
	}
	var f *Fault
	if errors.As(err, &f) {
		if f.Stage == "" {
			f.Stage = stage
		}
		if f.Source == "" {
			f.Source = source
		}
		if f.Offset == 0 {
			f.Offset = offset
		}
		return f
	}
	return &Fault{Stage: stage, Source: source, Offset: offset, Err: err}
}

func (f *Fault) Error() string {
	if f == nil || f.Err == nil {
		return fmt.Sprintf("%s fault in %q at offset %d", f.Stage, f.Source, f.Offset)
	}
	return fmt.Sprintf("%s fault in %q at offset %d: %v", f.Stage, f.Source, f.Offset, f.Err)
}

func (f *Fault) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Err
}

func AsFault(err error) (*Fault, bool) {
	var f *Fault
	ok := errors.As(err, &f)
	return f, ok
}
