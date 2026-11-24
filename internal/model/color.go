package model

// ColorMetadata carries named source values. A conflict never overwrites a
// source: both values remain in Sources and Conflicts.
type ColorMetadata struct {
	Primaries string `json:"primaries,omitempty"`
	Transfer  string `json:"transfer,omitempty"`
	Matrix    string `json:"matrix,omitempty"`
	Range     string `json:"range,omitempty"`
}

// SourcedColor is one color metadata statement and its origin.
type SourcedColor struct {
	Source   string        `json:"source"`
	Color    ColorMetadata `json:"color"`
	Offset   int64         `json:"offset,omitempty"`
}

// ColorConflict names a field with all source values that disagreed.
type ColorConflict struct {
	Field  string            `json:"field"`
	Values map[string]string `json:"values"`
}
