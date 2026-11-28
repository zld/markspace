package model

// Sourced retains an original metadata value together with the exact
// file offset and field path it was read from. Values are never silently
// overwritten: conflicting descriptions are kept side by side.
type Sourced[T any] struct {
	Value  T         `json:"value"`
	Source SourceRef `json:"source"`
}

// WithSource pairs a value with its provenance.
func WithSource[T any](v T, src SourceRef) Sourced[T] {
	return Sourced[T]{Value: v, Source: src}
}

// SourceOnly builds a SourceRef for the current read position.
func SourceOnly(file, path string, offset int64) SourceRef {
	return SourceRef{File: file, Path: path, Offset: offset}
}
