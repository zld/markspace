// Package adapter normalizes heterogeneous containers (MP4 files, RTP
// packet captures) into the unified media.Event stream defined by the
// media kernel.
package adapter

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"

	"mediad/internal/media"
)

// ParserAPIVersion is the parser interface version this build supports.
// Parsers declaring any other version are rejected at registration time —
// never silently probed with a mismatched ABI.
const ParserAPIVersion = "v1"

// Parser demultiplexes one container family into a Stream.
type Parser interface {
	// Name identifies the container family, e.g. "mp4", "rtpcap".
	Name() string
	// Version is the parser's interface version; must equal ParserAPIVersion.
	Version() string
	// Probe returns a confidence score > 0 if head looks like this
	// container, 0 otherwise. Higher wins.
	Probe(head []byte) int
	// Parse demultiplexes r into a Stream of unified events.
	Parse(r io.ReadSeeker) (*Stream, error)
}

// Track carries one source track's metadata plus its demuxed events.
type Track struct {
	ID          int
	Source      string // originating file, for diagnostics
	Type        media.TrackType
	Codec       string // fourcc or payload-type token; "" when unknown
	CodecParams []byte // raw parameter bytes, kept verbatim for audit
	Language    string // ISO-639-2; "" when undeclared
	Timescale   uint32
	Edits       []media.Edit
	StartOffset int64 // ns; non-zero when the media does not start at 0
	Events      []media.Event
	// DriftSegments is populated for clocked (RTP) sources.
	DriftSegments []media.DriftSegment
	// HasKeyframeTable is false when the container carried no random-access
	// table and keyframes were recovered by scanning payloads.
	HasKeyframeTable bool
}

// Stream is the demuxed content of one input file.
type Stream struct {
	Source string
	Tracks []*Track
	Gaps   []media.Gap // recoverable holes found during demux
}

// Registry holds the known parsers and routes inputs to them.
type Registry struct {
	parsers []Parser
}

// NewRegistry builds a registry, rejecting any parser whose interface
// version does not match ParserAPIVersion.
func NewRegistry(parsers ...Parser) (*Registry, error) {
	r := &Registry{}
	for _, p := range parsers {
		if err := r.Register(p); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Register adds a parser after a version check.
func (r *Registry) Register(p Parser) error {
	if p.Version() != ParserAPIVersion {
		return media.New(media.PhaseProbe, -1, -1,
			fmt.Sprintf("parser %q version %q does not match required %q",
				p.Name(), p.Version(), ParserAPIVersion), nil).Fatal()
	}
	for _, q := range r.parsers {
		if q.Name() == p.Name() {
			return media.New(media.PhaseProbe, -1, -1,
				fmt.Sprintf("parser %q registered twice", p.Name()), nil).Fatal()
		}
	}
	r.parsers = append(r.parsers, p)
	return nil
}

// probeSize is how many leading bytes are inspected for container sniffing.
const probeSize = 4096

// Parse probes the head of r, selects the best-matching parser and demuxes.
// An unrecognized container is a staged probe error, never a silent skip.
func (reg *Registry) Parse(source string, r io.ReadSeeker) (*Stream, error) {
	head := make([]byte, probeSize)
	n, _ := r.Read(head)
	head = head[:n]
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, media.New(media.PhaseProbe, -1, -1, "rewind after probe", err)
	}
	best, bestScore := Parser(nil), 0
	for _, p := range reg.parsers {
		if s := p.Probe(head); s > bestScore {
			best, bestScore = p, s
		}
	}
	if best == nil {
		return nil, media.New(media.PhaseProbe, -1, 0,
			fmt.Sprintf("unknown container: %s", source), nil).Fatal()
	}
	st, err := best.Parse(r)
	if err != nil {
		return nil, err
	}
	st.Source = source
	for _, t := range st.Tracks {
		t.Source = source
	}
	return st, nil
}

// CodecKey derives a stable identity for a track's codec configuration:
// the codec token plus a short digest of the raw parameter bytes. Tracks
// with missing parameters key on "raw" so they group together but stay
// distinct from any configured track.
func CodecKey(t *Track) string {
	if len(t.CodecParams) == 0 {
		return t.Codec + "/raw"
	}
	sum := sha1.Sum(t.CodecParams)
	return t.Codec + "/" + hex.EncodeToString(sum[:4])
}
