package render

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// Stats aggregates per-frame quality and size over a rendered sequence.
type Stats struct {
	Frames      int     `json:"frames"`
	ValidFrames int     `json:"valid_frames"`
	GapFrames   int     `json:"gap_frames"`
	QualityMean float64 `json:"quality_mean"`
	SampleBytes int64   `json:"sample_bytes"`
}

// HashFrame hashes one composited frame with its grid position, so the
// digest covers both content and placement on the shared timebase.
func HashFrame(index int, timeNanos int64, valid bool, data []byte) [32]byte {
	h := sha256.New()
	var hdr [17]byte
	binary.LittleEndian.PutUint64(hdr[0:8], uint64(index))
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(timeNanos))
	if valid {
		hdr[16] = 1
	}
	h.Write(hdr[:])
	h.Write(data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// HashHex formats a frame hash for manifests and logs.
func HashHex(h [32]byte) string { return hex.EncodeToString(h[:]) }

// Aggregate computes quality statistics over rendered frames.
func Aggregate(frames []Frame) Stats {
	var st Stats
	st.Frames = len(frames)
	var qsum float64
	for _, f := range frames {
		if f.Valid {
			st.ValidFrames++
		} else {
			st.GapFrames++
		}
		qsum += f.Quality
		st.SampleBytes += int64(f.Size)
	}
	if st.Frames > 0 {
		st.QualityMean = qsum / float64(st.Frames)
	}
	return st
}
