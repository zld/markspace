package media

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/zld/media-regress/internal/error"
)

// Parse sniffs the container, parses it and returns a normalized Asset. The
// digest is the SHA-256 of the input bytes and is always populated, even when
// parsing fails, so callers can key retries and caches by content.
func Parse(data []byte, source string) (*Asset, error) {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if probeMXF(data) {
		return parseMXF(data, source, digest)
	}
	if probeMP4(data) {
		return parseMP4(data, source, digest)
	}
	return nil, merr.New(merr.StageProbe, merr.KindParse, 0, 0,
		"unrecognized container (want MXF or MP4, %d bytes)", len(data))
}

// Accounting returns the number of tracks by kind in a stable order.
func (a *Asset) Accounting() (video, audio int) {
	for _, t := range a.Tracks {
		switch t.Kind {
		case KindVideo:
			video++
		case KindAudio:
			audio++
		}
	}
	return
}
