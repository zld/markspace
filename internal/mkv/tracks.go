package mkv

import (
	"fmt"
	"sort"

	"tracecheck/internal/ebml"
	"tracecheck/internal/serr"
)

// Track types defined by Matroska.
const (
	TrackVideo    uint64 = 1
	TrackAudio    uint64 = 2
	TrackSubtitle uint64 = 17
)

// Explicit defaults applied when a track omits an optional field. They are
// reported as Default rather than silently invented so downstream summaries
// can distinguish a present value from a substituted one.
const (
	DefaultTimestampScale = 1_000_000 // 1 ms in nanoseconds
	DefaultSamplingFreq   = 8000.0    // Hz
	DefaultChannels       = 1
	DefaultBitDepth       = 0
	DefaultDisplayUnit    = 0
)

// Field is one optional metadata value together with whether it was present.
type Field[T any] struct {
	Value     T
	Present   bool
	Defaulted bool // Value is an explicit default, not read from the file
}

// Conflict records two contradictory values for the same element position
// (for example two Audio/Channels children). Bytes are never rewritten; both
// the original value and the conflicting value are kept.
type Conflict struct {
	Field    string
	Kept     string // original value retained in the track
	Rejected string // later value that contradicted it
	Offset   int64  // absolute offset of the rejected element
}

// Colour describes the colour metadata subset needed for deterministic reads.
type Colour struct {
	MatrixCoefficients      Field[uint64]
	Range                   Field[uint64]
	TransferCharacteristics Field[uint64]
	Primaries               Field[uint64]
	BitsPerChannel          Field[uint64]
}

// Audio describes the audio parameters of a track.
type Audio struct {
	SamplingFrequency Field[float64]
	Channels          Field[uint64]
	BitDepth          Field[uint64]
	ChannelPositions  Field[string] // raw Matroska ChannelPositions bytes as text
}

// Video describes the video parameters of a track.
type Video struct {
	PixelWidth    Field[uint64]
	PixelHeight   Field[uint64]
	DisplayWidth  Field[uint64]
	DisplayHeight Field[uint64]
	DisplayUnit   Field[uint64]
	Colour        Colour
}

// Track is one TrackEntry with its resolved parameters.
type Track struct {
	Number      uint64
	UID         Field[uint64]
	Type        Field[uint64]
	CodecID     Field[string]
	Name        Field[string]
	Language    Field[string]
	FlagDefault Field[uint64]
	Audio       Audio
	Video       Video
	Offset      int64 // absolute offset of the TrackEntry element
	Conflicts   []Conflict
}

// Tracks is the parsed Tracks element plus the document level scale.
type Tracks struct {
	Items          []*Track
	TimestampScale int64 // nanoseconds per Matroska tick
	ScaleDefaulted bool
}

// ByNumber returns the track with the given number, or nil.
func (t *Tracks) ByNumber(n uint64) *Track {
	for _, tr := range t.Items {
		if tr.Number == n {
			return tr
		}
	}
	return nil
}

// ParseTracks reads Segment/Info and Segment/Tracks. Missing optional values
// receive the documented defaults; contradictory duplicates are recorded in
// Track.Conflicts while the first value read is retained.
func ParseTracks(doc *ebml.Document) (*Tracks, error) {
	out := &Tracks{TimestampScale: DefaultTimestampScale}
	if info := doc.Find(ebml.IDSegment, ebml.IDInfo); info != nil {
		for _, child := range doc.Children[info] {
			if child.ID != ebml.IDTimecodeScale {
				continue
			}
			v, ok := ebml.ReadUint(doc.Payload(child))
			if !ok {
				return nil, serr.New(serr.CodeIllegalVINT, "TimestampScale payload has invalid width").
					At(child.Offset).WithPath(pathNames(doc, child))
			}
			out.TimestampScale = int64(v)
			break
		}
	} else {
		out.ScaleDefaulted = true
	}
	if out.TimestampScale <= 0 {
		return nil, serr.New(serr.CodeIllegalVINT,
			"TimestampScale must be positive, got %d", out.TimestampScale).
			WithPath([]string{"Segment", "Info", "TimestampScale"})
	}

	tracksEl := doc.Find(ebml.IDSegment, ebml.IDTracks)
	if tracksEl == nil {
		return out, nil
	}
	for _, entry := range doc.Children[tracksEl] {
		if entry.ID != ebml.IDTrackEntry {
			continue
		}
		track, err := parseTrackEntry(doc, entry)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, track)
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		return out.Items[i].Number < out.Items[j].Number
	})
	return out, nil
}

func parseTrackEntry(doc *ebml.Document, entry *ebml.Element) (*Track, error) {
	tr := &Track{Offset: entry.Offset}
	seen := map[ebml.ElementID]*ebml.Element{}
	for _, c := range doc.Children[entry] {
		// Track the first occurrence of every direct scalar for conflict checks.
		if _, dup := seen[c.ID]; dup {
			tr.noteConflict(doc, c, ebml.NameOf(c.ID))
			continue
		}
		seen[c.ID] = c
		switch c.ID {
		case ebml.IDTrackNumber:
			v, ok := ebml.ReadUint(doc.Payload(c))
			if !ok {
				return nil, serr.New(serr.CodeIllegalVINT, "TrackNumber width invalid").
					At(c.Offset).WithPath(pathNames(doc, c))
			}
			tr.Number = v
		case ebml.IDTrackUID:
			tr.UID = fieldUint(doc, c)
		case ebml.IDTrackType:
			tr.Type = fieldUint(doc, c)
		case ebml.IDCodecID:
			tr.CodecID = Field[string]{Value: ebml.ReadString(doc.Payload(c)), Present: true}
		case ebml.IDName:
			tr.Name = Field[string]{Value: ebml.ReadString(doc.Payload(c)), Present: true}
		case ebml.IDLanguage:
			tr.Language = Field[string]{Value: ebml.ReadString(doc.Payload(c)), Present: true}
		case ebml.IDFlagDefault:
			tr.FlagDefault = fieldUint(doc, c)
		case ebml.IDAudio:
			parseAudio(doc, c, &tr.Audio, &tr.Conflicts)
		case ebml.IDVideo:
			parseVideo(doc, c, &tr.Video, &tr.Conflicts)
		}
	}
	if err := tr.applyDefaults(); err != nil {
		return nil, err
	}
	return tr, nil
}

func (t *Track) applyDefaults() error {
	if t.Number == 0 {
		return serr.New(serr.CodeIllegalVINT, "TrackEntry is missing a nonzero TrackNumber").
			At(t.Offset)
	}
	if !t.Audio.SamplingFrequency.Present {
		t.Audio.SamplingFrequency = Field[float64]{
			Value: DefaultSamplingFreq, Defaulted: true}
	}
	if !t.Audio.Channels.Present {
		t.Audio.Channels = Field[uint64]{Value: DefaultChannels, Defaulted: true}
	}
	if !t.Audio.BitDepth.Present {
		t.Audio.BitDepth = Field[uint64]{Value: DefaultBitDepth, Defaulted: true}
	}
	if !t.Video.DisplayUnit.Present {
		t.Video.DisplayUnit = Field[uint64]{Value: DefaultDisplayUnit, Defaulted: true}
	}
	return nil
}

func parseAudio(doc *ebml.Document, audio *ebml.Element, out *Audio, conflicts *[]Conflict) {
	seen := map[ebml.ElementID]*ebml.Element{}
	for _, c := range doc.Children[audio] {
		if _, dup := seen[c.ID]; dup {
			*conflicts = append(*conflicts, makeConflict(doc, c, ebml.NameOf(c.ID)))
			continue
		}
		seen[c.ID] = c
		switch c.ID {
		case ebml.IDSamplingFrequency:
			if v, ok := ebml.ReadFloat64(doc.Payload(c)); ok {
				out.SamplingFrequency = Field[float64]{Value: v, Present: true}
			}
		case ebml.IDChannels:
			out.Channels = fieldUint(doc, c)
		case ebml.IDBitDepth:
			out.BitDepth = fieldUint(doc, c)
		case ebml.IDChannelPositions:
			out.ChannelPositions = Field[string]{
				Value: ebml.ReadString(doc.Payload(c)), Present: true}
		}
	}
}

func parseVideo(doc *ebml.Document, video *ebml.Element, out *Video, conflicts *[]Conflict) {
	seen := map[ebml.ElementID]*ebml.Element{}
	for _, c := range doc.Children[video] {
		if _, dup := seen[c.ID]; dup {
			*conflicts = append(*conflicts, makeConflict(doc, c, ebml.NameOf(c.ID)))
			continue
		}
		seen[c.ID] = c
		switch c.ID {
		case ebml.IDPixelWidth:
			out.PixelWidth = fieldUint(doc, c)
		case ebml.IDPixelHeight:
			out.PixelHeight = fieldUint(doc, c)
		case ebml.IDDisplayWidth:
			out.DisplayWidth = fieldUint(doc, c)
		case ebml.IDDisplayHeight:
			out.DisplayHeight = fieldUint(doc, c)
		case ebml.IDDisplayUnit:
			out.DisplayUnit = fieldUint(doc, c)
		case ebml.IDColour:
			parseColour(doc, c, &out.Colour, conflicts)
		}
	}
}

func parseColour(doc *ebml.Document, colour *ebml.Element, out *Colour, conflicts *[]Conflict) {
	seen := map[ebml.ElementID]*ebml.Element{}
	for _, c := range doc.Children[colour] {
		if _, dup := seen[c.ID]; dup {
			*conflicts = append(*conflicts, makeConflict(doc, c, ebml.NameOf(c.ID)))
			continue
		}
		seen[c.ID] = c
		switch c.ID {
		case ebml.IDMatrixCoeffs:
			out.MatrixCoefficients = fieldUint(doc, c)
		case ebml.IDRange:
			out.Range = fieldUint(doc, c)
		case ebml.IDTransferChars:
			out.TransferCharacteristics = fieldUint(doc, c)
		case ebml.IDPrimaries:
			out.Primaries = fieldUint(doc, c)
		case ebml.IDBitsPerChannel:
			out.BitsPerChannel = fieldUint(doc, c)
		}
	}
}

func fieldUint(doc *ebml.Document, el *ebml.Element) Field[uint64] {
	v, ok := ebml.ReadUint(doc.Payload(el))
	if !ok {
		return Field[uint64]{}
	}
	return Field[uint64]{Value: v, Present: true}
}

func makeConflict(doc *ebml.Document, el *ebml.Element, name string) Conflict {
	return Conflict{
		Field:    name,
		Rejected: renderPayload(doc.Payload(el)),
		Offset:   el.Offset,
	}
}

func (t *Track) noteConflict(doc *ebml.Document, el *ebml.Element, name string) {
	c := makeConflict(doc, el, name)
	t.Conflicts = append(t.Conflicts, c)
}

// renderPayload formats a rejected value for the conflict record without
// altering the underlying bytes.
func renderPayload(p []byte) string {
	if v, ok := ebml.ReadUint(p); ok {
		return fmt.Sprintf("uint:%d", v)
	}
	if v, ok := ebml.ReadFloat64(p); ok {
		return fmt.Sprintf("float:%g", v)
	}
	return fmt.Sprintf("bytes:%x", p)
}
