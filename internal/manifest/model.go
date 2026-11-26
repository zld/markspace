package manifest

import (
	"fmt"
	"os"
	"strings"

	"medtrace/internal/core"
)

// Kind distinguishes the two manifest dialects.
type Kind string

const (
	// DASHKind is a static DASH MPD.
	DASHKind Kind = "dash"
	// HLSKind is an HLS media playlist.
	HLSKind Kind = "hls"
)

// Representation describes one DASH representation (or one HLS rendition when
// a master playlist is supported in future). ID is empty for single-rate HLS.
type Representation struct {
	ID        string
	Bandwidth int64
	Codecs    string
	Width     uint32
	Height    uint32
	Track     core.TrackKind
	// Init is the root-relative initialization segment, empty when absent.
	Init string
	// Timescale is the media timescale declared by the segment template or
	// zero when it must be read from the init segment.
	Timescale uint32
}

// Segment is one media fragment in manifest order. Its URI and Init are
// root-relative and verified to stay inside the media root at parse time.
type Segment struct {
	// URI is the root-relative media path.
	URI  string
	Line int
	// Representation is the ID of the owning representation ("" for HLS).
	Representation string
	// Init is the root-relative init segment for this fragment.
	Init string
	// Duration is the nominal duration declared by the manifest; zero when
	// the manifest does not say.
	Duration core.Nanos
	// Sequence is the HLS media sequence number or DASH segment number.
	Sequence int64
	// Discontinuity reports that an HLS #EXT-X-DISCONTINUITY immediately
	// precedes this segment.
	Discontinuity bool
	// AnchorUnixNanos is the program date-time anchor in Unix nanoseconds,
	// or zero when the manifest provides none.
	AnchorUnixNanos int64
}

// Playlist is the ordered media plan produced by a manifest parser. Segments
// appear in document order, including across DASH representations, so a
// representation switch is visible to the epoch tracker as a normal order
// change rather than something the caller must reconstruct.
type Playlist struct {
	Kind            Kind
	File            string
	Representations []Representation
	Segments        []Segment
}

// RepByID looks up a representation by ID.
func (p *Playlist) RepByID(id string) (Representation, bool) {
	for _, r := range p.Representations {
		if r.ID == id {
			return r, true
		}
	}
	return Representation{}, false
}

// Load reads and parses the manifest at rel (relative to root). The returned
// error is always a *core.Fault carrying the manifest path and, where the
// dialect is line-oriented or trackable, the offending line.
func Load(root, rel string) (*Playlist, error) {
	abs, err := Abs(root, rel)
	if err != nil {
		return nil, core.NewFault(core.KindPath, core.Pos{File: rel, Byte: -1}, "load", err.Error())
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, core.WrapFault(err, core.KindIO, core.Pos{File: rel, Byte: -1}, "load", "read manifest")
	}
	return Parse(rel, data)
}

// Parse parses manifest bytes whose path relative to the media root is rel.
func Parse(rel string, data []byte) (*Playlist, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil, core.NewFault(core.KindManifest, core.Pos{File: rel, Line: 1, Byte: -1}, "parse", "manifest is empty")
	}
	if strings.HasPrefix(text, "<") {
		return parseDASH(rel, data)
	}
	if strings.HasPrefix(text, "#EXTM3U") {
		return parseHLS(rel, data)
	}
	first := strings.SplitN(text, "\n", 2)[0]
	if len(first) > 40 {
		first = first[:40] + "..."
	}
	return nil, core.NewFault(core.KindManifest, core.Pos{File: rel, Line: 1, Byte: -1}, "parse",
		fmt.Sprintf("unrecognised manifest header %q (want XML MPD or #EXTM3U)", first))
}
