package model

// TrackID identifies one input track. Track 0 is reserved and rejected so a
// zero value can never be mistaken for a valid track.
type TrackID int

// Frame is the ingest-facing description of one media frame. All times are
// nanoseconds on the shared base; Size is the encoded byte length.
type Frame struct {
	Track    TrackID   `json:"track"`
	Sequence uint32    `json:"sequence"`
	PTS      Timestamp `json:"pts"`
	Size     int       `json:"size"`
	Color    Color     `json:"color"`
	Path     string    `json:"path"`
	SHA256   string    `json:"sha256"`
}

// Color carries the colour metadata declared in the manifest. Values must be
// inside the BT.709 / sRGB primaries envelope.
type Color struct {
	Primaries string `json:"primaries"`
	Transfer  string `json:"transfer"`
	Range     string `json:"range"`
}

// ValidColors lists the accepted colour enums.
var ValidColors = map[string]map[string]bool{
	"primaries": {"bt709": true},
	"transfer":  {"bt709": true},
	"range":     {"full": true, "limited": true},
}

// ColorValid reports whether every colour field is a known value.
func ColorValid(c Color) bool {
	return ValidColors["primaries"][c.Primaries] &&
		ValidColors["transfer"][c.Transfer] &&
		ValidColors["range"][c.Range]
}

// SortKey orders frames deterministically: timestamp first, then track id,
// then sequence. Callers never rely on map or input order.
func SortKey(f Frame) [5]int64 {
	return [5]int64{
		int64(f.PTS), int64(f.Track), int64(f.Sequence),
		int64(f.Size), int64(len(f.Path)),
	}
}

// MaxDimension is the hard pixel-size bound for a single frame side.
const MaxDimension = 8192

// MaxFrameBytes bounds the encoded payload of one frame.
const MaxFrameBytes = 16 << 20

// Validate checks frame metadata without touching the filesystem or hashes.
func (f Frame) Validate() error {
	if f.Track <= 0 {
		return NewFault(KindInput, CodeTrackInvalid, f.Track, f.Sequence, "track id must be positive")
	}
	if f.Sequence == 0 {
		return NewFault(KindInput, CodeSeqInvalid, f.Track, f.Sequence, "sequence numbers start at 1")
	}
	if err := f.PTS.Validate(); err != nil {
		return WithFault(faultOf(err), f.Track, f.Sequence)
	}
	if f.Size < 0 || f.Size > MaxFrameBytes {
		return NewFault(KindInput, CodeSizeBounds, f.Track, f.Sequence,
			"frame byte size out of bounds")
	}
	if f.Path == "" {
		return NewFault(KindInput, CodePathInvalid, f.Track, f.Sequence, "empty frame path")
	}
	if len(f.SHA256) != 64 {
		return NewFault(KindInput, CodeHashMalformed, f.Track, f.Sequence, "SHA-256 must be 64 hex characters")
	}
	for _, r := range f.SHA256 {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return NewFault(KindInput, CodeHashMalformed, f.Track, f.Sequence, "SHA-256 must be lowercase hex")
		}
	}
	if !ColorValid(f.Color) {
		return NewFault(KindInput, CodeColorInvalid, f.Track, f.Sequence, "unknown colour metadata")
	}
	return nil
}
