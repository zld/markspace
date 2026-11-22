package model

// TrackID is the stable identity of a late-arriving media or metadata track.
// Track IDs are chosen by the ingest layer and never reused in one run.
type TrackID string

// Metadata track identifiers are deterministic constants so the generator and
// the demo can agree on them without extra plumbing.
const (
	TrackVideoMain TrackID = "v-main"
	TrackAudioMain TrackID = "a-main"
	TrackSubtitles TrackID = "s-sub"
	TrackChapters  TrackID = "s-chap"
	TrackID3       TrackID = "m-id3"
	TrackEmsg      TrackID = "m-emsg"
	TrackSEI       TrackID = "m-sei"
)

// MediaKind classifies a track for the ladder and recovery stages.
type MediaKind string

// Media kinds.
const (
	KindVideo   MediaKind = "video"
	KindAudio   MediaKind = "audio"
	KindText    MediaKind = "text"
	KindChapter MediaKind = "chapter"
	KindTimed   MediaKind = "timed-metadata"
)

// Track identifies a source track entering the pipeline.
type Track struct {
	ID        TrackID   `json:"id"`
	Kind      MediaKind `json:"kind"`
	Timescale uint32    `json:"timescale"`
	// OriginOffset is the declared start of this track relative to the common
	// program clock. It is repaired, never blindly trusted.
	OriginOffset float64 `json:"-"`
}

// RationalTime mirrors clock.Rational without importing it, so the model
// package stays dependency-free. The exact value is num/den seconds.
type RationalTime struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// TimeUnit converts between raw integer ticks and rational seconds.
type TimeUnit struct {
	Timescale uint32
}

// Valid reports whether the time unit can be used.
func (u TimeUnit) Valid() bool { return u.Timescale != 0 }

// DecodeState records how a raw timestamp was turned into repaired time.
type DecodeState string

// Decode states.
const (
	DecodeExact      DecodeState = "exact"
	DecodeRepaired   DecodeState = "repaired"
	DecodeForwarded  DecodeState = "forwarded"
	DecodeRejected   DecodeState = "rejected"
	DecodeMissing    DecodeState = "missing"
	DecodeDuplicated DecodeState = "duplicated"
)

// TimeStamp is one raw timestamp and its repaired counterpart. RawTime is the
// original integer tick value (or -1 when the input carried no timestamp).
// Both are kept so the demo can measure round-trip agreement.
type TimeStamp struct {
	RawTicks     int64        `json:"raw_ticks"`
	RawTime      RationalTime `json:"raw_time"`
	RepairedTime RationalTime `json:"repaired_time"`
	State        DecodeState  `json:"state"`
	// Epoch is the number of 32-bit wraps applied while repairing.
	Epoch int64 `json:"epoch"`
}

// Sample is a single decodable unit from an MP4 sample table or an RTP
// access unit after reassembly.
type Sample struct {
	Index             int          `json:"index"`
	Track             TrackID      `json:"track"`
	Time              TimeStamp    `json:"time"`
	Duration          RationalTime `json:"duration"`
	Size              uint32       `json:"size"`
	Sync              bool         `json:"sync"`
	CompositionOffset RationalTime `json:"composition_offset"`
}

// MetadataKind names a timed metadata carrier.
type MetadataKind string

// Timed metadata kinds.
const (
	MetaID3      MetadataKind = "id3"
	MetaEmsg     MetadataKind = "emsg"
	MetaSEI      MetadataKind = "sei"
	MetaSubtitle MetadataKind = "subtitle"
	MetaChapter  MetadataKind = "chapter"
)

// Event is one timed metadata event (subtitle cue, chapter, ID3 tag, emsg or
// SEI message) with its raw and repaired timestamps.
type Event struct {
	Kind     MetadataKind `json:"kind"`
	Track    TrackID      `json:"track"`
	ID       string       `json:"id"`
	Time     TimeStamp    `json:"time"`
	Duration RationalTime `json:"duration"`
	Scheme   string       `json:"scheme,omitempty"`
	Value    string       `json:"value,omitempty"`
	Text     string       `json:"text,omitempty"`
	Payload  []byte       `json:"-"`
	// ValidEnd is the repaired end time; subtitles and chapters require
	// start < end and an end inside the program timeline.
	ValidEnd RationalTime `json:"valid_end"`
}
