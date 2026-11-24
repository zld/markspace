// Package adapter hosts versioned media parsers. Only parsers explicitly
// registered here are ever invoked; the kernel does not attempt to decode
// arbitrary codecs or unrecognized container layouts.
package adapter

import (
	"path/filepath"
	"strings"

	"mediad/internal/model"
)

// MediaInput tells a parser which file to read and where it was referenced.
type MediaInput struct {
	Path          string         // absolute or working-directory-relative file
	Representation string        // owning representation id (provenance)
	Entry         int            // segment index inside the representation
	Manifest      string         // owning manifest path
}

// MediaParser is the versioned parser contract. Bump Version when output
// semantics change: checkpoints are keyed on it so old state never mixes
// with results produced by a different parser.
type MediaParser interface {
	Format() string // "mp4" | "rtp"
	Version() string
	Parse(in MediaInput) (*model.MediaTrack, error)
}

var registry = map[string]MediaParser{}

// Register installs a parser. Registering the same format twice is a
// programming error and panics rather than silently overriding.
func Register(p MediaParser) {
	if _, exists := registry[p.Format()]; exists {
		panic("adapter: duplicate parser registration for " + p.Format())
	}
	registry[p.Format()] = p
}

// Get returns the registered parser for a format name.
func Get(format string) (MediaParser, error) {
	p, ok := registry[format]
	if !ok {
		return nil, model.NewError(model.KindUnregisteredParser,
			model.SourceLoc{Entry: -1, Frame: -1, Offset: -1},
			"no registered parser for format "+format)
	}
	return p, nil
}

// FormatForPath derives the registered format from a file extension.
// Fragmented-MP4 appears under both .mp4 and .m4s in the fixtures.
func FormatForPath(path string) string {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "m4s":
		return "mp4"
	default:
		return strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}
}

// Parse dispatches a media file to its registered versioned parser.
func Parse(in MediaInput) (*model.MediaTrack, error) {
	p, err := Get(FormatForPath(in.Path))
	if err != nil {
		se, _ := model.AsSource(err)
		se.Loc.Segment = in.Path
		se.Loc.Representation = in.Representation
		se.Loc.Entry = in.Entry
		return nil, se
	}
	return p.Parse(in)
}

// RegisteredVersions maps format -> parser version for provenance records.
func RegisteredVersions() map[string]string {
	out := map[string]string{}
	for f, p := range registry {
		out[f] = p.Version()
	}
	return out
}
