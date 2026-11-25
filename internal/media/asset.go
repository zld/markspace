// Package media holds the container-neutral description of a delivery asset
// produced by the Matroska and MP4 adapters. It is the single input to track
// validation, fragment validation, manifest migration and the CLI report.
package media

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"

	"example.com/assetcheck/internal/timeline"
	"example.com/assetcheck/internal/tracks"
)

// Container identifies the file format.
type Container string

const (
	ContainerMatroska Container = "matroska"
	ContainerWebM     Container = "webm"
	ContainerMP4      Container = "mp4"
)

// Fragment is one media block, normalized to the media time base. Interleaved
// blocks keep their original document order in Order, and Seq is the transport
// sequence from a container-level index when present (MP4 sidx or an RTP
// derived manifest); -1 means the container carries no sequence.
type Fragment struct {
	TrackNumber uint64
	Order       int
	Seq         int64
	Start       timeline.Nanosecond
	Duration    timeline.Nanosecond
	// Reference is the container's shared instant for the fragment's cluster.
	// Cross-track alignment compares each track's first fragment in a cluster
	// against this reference.
	Reference timeline.Nanosecond
	Cluster   int
	Offset    int64 // absolute input offset of the block element
	Keyframe  bool
	Kind      string
}

// End returns the fragment's exclusive end instant.
func (f Fragment) End() timeline.Nanosecond { return f.Start + f.Duration }

// Element is a flattened record of one container element with its raw offset.
type Element struct {
	ID     uint32
	Name   string
	Offset int64
	Size   uint64
}

// Asset is the parsed, container-neutral view of one input file.
type Asset struct {
	Path          string
	Container     Container
	Size          int64
	SHA256        string
	DocType       string
	Version       uint64
	TimecodeScale uint64 // nanoseconds per Matroska tick, or 1
	Clock         timeline.Clock
	Duration      timeline.Nanosecond // 0 when absent
	Tracks        []tracks.Track
	Fragments     []Fragment
	Elements      []Element
	// UnknownSized counts masters parsed with unknown payload size.
	UnknownSized int
	// SequenceBase is the expected first transport sequence, or -1.
	SequenceBase int64
}

// ElementCount returns the number of recorded elements.
func (a *Asset) ElementCount() int { return len(a.Elements) }

// TrackByNumber returns a pointer to the track with the given number.
func (a *Asset) TrackByNumber(n uint64) *tracks.Track {
	for i := range a.Tracks {
		if a.Tracks[i].Number == n {
			return &a.Tracks[i]
		}
	}
	return nil
}

// HashFile computes the SHA-256 of a file using a bounded buffer, returning the
// digest and byte count without loading the file.
func HashFile(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.CopyBuffer(h, r, make([]byte, 64<<10))
	if err != nil {
		return "", n, timeline.Wrap(err, timeline.StageValidate, timeline.CatIO, 0, -1,
			"hash input")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// SortFragmentsByStart orders fragments by media start then original order, for
// timeline reporting. Validation uses Order to check interleaving separately.
func SortFragmentsByStart(fs []Fragment) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Start != fs[j].Start {
			return fs[i].Start < fs[j].Start
		}
		return fs[i].Order < fs[j].Order
	})
}

// TrackNumbers returns the sorted track numbers present in fragments.
func (a *Asset) TrackNumbers() []uint64 {
	set := map[uint64]bool{}
	for _, f := range a.Fragments {
		set[f.TrackNumber] = true
	}
	out := make([]uint64, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
