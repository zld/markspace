// Package fixture generates local, self-contained media segment samples so
// the pipeline can be exercised without external media files.
package fixture

import (
	"crypto/sha256"
	"encoding/binary"

	"mediaseal/internal/media"
)

const (
	videoDuration = 4000 // ticks per video segment
	audioDuration = 2048 // ticks per audio segment
	plaintextSize = 256  // bytes of synthetic payload per segment
)

// Generate builds count deterministic segments for assetID, alternating
// between the video and audio tracks. Each segment records its number,
// track, PTS, duration and plaintext digest.
func Generate(assetID string, count int) []media.Segment {
	segs := make([]media.Segment, 0, count)
	var videoPTS, audioPTS int64
	for i := 0; i < count; i++ {
		track := media.TrackVideo
		pts := videoPTS
		dur := int64(videoDuration)
		if i%2 == 1 {
			track = media.TrackAudio
			pts = audioPTS
			dur = audioDuration
		}
		plain := payload(assetID, track, i)
		segs = append(segs, media.Segment{
			AssetID:   assetID,
			Track:     track,
			Index:     i,
			PTS:       pts,
			Duration:  dur,
			Plaintext: plain,
			Digest:    sha256.Sum256(plain),
		})
		if track == media.TrackVideo {
			videoPTS += dur
		} else {
			audioPTS += dur
		}
	}
	return segs
}

// payload derives deterministic synthetic bytes from the segment identity.
func payload(assetID string, track media.Track, index int) []byte {
	h := sha256.New()
	h.Write([]byte(assetID))
	h.Write([]byte{0})
	h.Write([]byte(track))
	var idx [8]byte
	binary.BigEndian.PutUint64(idx[:], uint64(index))
	h.Write(idx[:])
	seed := h.Sum(nil)
	out := make([]byte, 0, plaintextSize)
	for len(out) < plaintextSize {
		sum := sha256.Sum256(seed)
		seed = sum[:]
		out = append(out, seed...)
	}
	return out[:plaintextSize]
}
