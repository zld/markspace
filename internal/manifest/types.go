// Package manifest parses local HLS playlists and DASH MPDs into an ordered,
// source-located representation. Parsing never returns a partial playlist:
// either the whole manifest is valid or a located error is returned.
package manifest

import "archive-lab/internal/media"

// Kind identifies which manifest dialect produced a Playlist.
type Kind int

const (
	KindMedia Kind = iota + 1
	KindMaster
	KindDASH
)

func (k Kind) String() string {
	switch k {
	case KindMedia:
		return "hls-media"
	case KindMaster:
		return "hls-master"
	case KindDASH:
		return "dash"
	default:
		return "unknown"
	}
}

// ByteRange is an HLS EXT-X-BYTERANGE window. A negative Offset means the
// range starts immediately after the previous segment.
type ByteRange struct {
	Offset int64
	Length int64
}

// Segment is one media segment. StartNs is the accumulated boundary and is
// always exactly StartNs+DurationNs of its predecessor; Line is 1-based.
type Segment struct {
	Index         int
	URI           string
	Line          int
	StartNs       int64
	DurationNs    int64
	Sequence      int64
	Range         *ByteRange
	Discontinuity bool
}

// Variant is one EXT-X-STREAM-INF entry of a master playlist.
type Variant struct {
	Line             int
	URI              string
	Bandwidth        int64
	AverageBandwidth int64
	Codecs           string
	Width            int
	Height           int
	FrameRate        string
	AudioGroup       string
	VideoGroup       string
	SubtitleGroup    string
}

// Rendition is one EXT-X-MEDIA entry of a master playlist.
type Rendition struct {
	Line       int
	Type       string
	GroupID    string
	Name       string
	Language   string
	URI        string
	Default    bool
	Autoselect bool
	Forced     bool
}

// Representation is a DASH representation (one rendition layer) with its
// segment list already expanded.
type Representation struct {
	ID                string
	Bandwidth         int64
	AverageBandwidth  int64
	Codecs            string
	MimeType          string
	Width             int
	Height            int
	FrameRate         string
	AudioSamplingRate int
	Language          string
	Timescale         uint32
	InitURI           string
	Segments          []Segment
	TotalNs           int64
}

// Playlist is the parsed result. Exactly one of Segments (media), Variants
// (master) or Representations (DASH) is populated according to Kind.
type Playlist struct {
	Path             string
	Kind             Kind
	Version          int
	TargetDurationNs int64
	MediaSequence    int64
	PlaylistType     string
	EndList          bool
	Segments         []Segment
	TotalNs          int64
	Variants         []Variant
	Renditions       []Rendition
	Representations  []Representation
	BaseURL          string
}

// duration builds a Segment with the running start boundary.
func appendSegment(dst []Segment, uri string, line int, start, dur, seq int64, rng *ByteRange, disc bool) ([]Segment, int64, *media.Error) {
	if dur <= 0 {
		return nil, 0, media.New(media.StageParse, "segment", "segment duration must be positive, got %dns", dur).
			WithLine(line).WithSegment(len(dst))
	}
	end := start + dur
	if end < start {
		return nil, 0, media.New(media.StageParse, "segment", "cumulative duration overflow at segment %d", len(dst)).
			WithLine(line).WithSegment(len(dst))
	}
	return append(dst, Segment{
		Index: len(dst), URI: uri, Line: line, StartNs: start, DurationNs: dur,
		Sequence: seq, Range: rng, Discontinuity: disc,
	}), end, nil
}
