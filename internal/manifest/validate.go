package manifest

import (
	"strings"

	"archivecheck/internal/model"
)

// Validator accumulates parse errors for one manifest text. It tracks the
// current line/column so each fault can point at its source position.
type Validator struct {
	file   string
	line   int
	col    int
	faults model.FaultList
	// earliest is the first fatal fault in source order, reported as the
	// headline while the complete list stays on the cause.
	earliest *model.Fault
}

// NewValidator starts validation of file at line 1, column 1.
func NewValidator(file string) *Validator {
	return &Validator{file: file, line: 1, col: 1}
}

// At records the current 1-based source position.
func (v *Validator) At(line, col int) { v.line, v.col = line, col }

// Faults returns the accumulated faults in deterministic order.
func (v *Validator) Faults() []*model.Fault { return v.faults.All() }

// Err returns a fatal manifest error wrapping all accumulated faults, or nil.
func (v *Validator) Err() error {
	if v.faults.Len() == 0 {
		return nil
	}
	// Report the earliest fault in source order as the headline so callers see
	// the first thing that went wrong; every detail is on the cause.
	all := v.faults.All()
	first := v.earliest
	if first == nil {
		first = all[0]
	}
	return model.NewFault(first.Code, model.SeverityError, model.Source{
		Layer: "manifest", File: v.file, Line: first.Source.Line, Column: first.Source.Column, Offset: -1,
	}, "%d manifest error(s); first: %s", len(all), first.Message).
		WithCause(&MultiError{Faults: all})
}

// MultiError carries several source-positioned faults from one parse.
type MultiError struct {
	Faults []*model.Fault
}

// Error joins all fault messages.
func (m *MultiError) Error() string {
	parts := make([]string, 0, len(m.Faults))
	for _, f := range m.Faults {
		parts = append(parts, f.Error())
	}
	return strings.Join(parts, "; ")
}

// Errors returns each fault as an error.
func (m *MultiError) Errors() []error {
	out := make([]error, 0, len(m.Faults))
	for _, f := range m.Faults {
		out = append(out, f)
	}
	return out
}

func (v *Validator) fail(code model.Code, format string, args ...any) {
	src := model.Source{Layer: "manifest", File: v.file, Line: v.line, Column: v.col, Offset: -1}
	f := model.NewFault(code, model.SeverityError, src, format, args...)
	v.faults.Add(f)
	if v.earliest == nil {
		v.earliest = f
	}
}

func (v *Validator) warn(code model.Code, format string, args ...any) {
	src := model.Source{Layer: "manifest", File: v.file, Line: v.line, Column: v.col, Offset: -1}
	v.faults.Add(model.NewFault(code, model.SeverityWarn, src, format, args...))
}
