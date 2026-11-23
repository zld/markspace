package render

import (
	"crypto/sha256"
	"encoding/hex"
)

// FrameHash binds one frame's timestamp to the SHA-256 of its PNG bytes.
type FrameHash struct {
	Index  int
	TimeNs int64
	SHA256 string
	Bytes  int
}

// HashFrames computes a deterministic manifest: each PNG is hashed in frame
// order. Identical input frames always produce identical hashes.
func HashFrames(frames []Frame) []FrameHash {
	out := make([]FrameHash, len(frames))
	for i, f := range frames {
		sum := sha256.Sum256(f.PNG)
		out[i] = FrameHash{
			Index:  f.Index,
			TimeNs: f.TimeNs,
			SHA256: hex.EncodeToString(sum[:]),
			Bytes:  len(f.PNG),
		}
	}
	return out
}

// ManifestDigest returns a single digest over the whole ordered manifest,
// so two runs can be compared by one string.
func ManifestDigest(hashes []FrameHash) string {
	h := sha256.New()
	for _, fh := range hashes {
		h.Write([]byte(fh.SHA256))
		h.Write(byteSlice(fh.TimeNs))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// AudioStats summarizes recovered audio without decoding the codec stream.
type AudioStats struct {
	TrackNumber  uint32
	SampleCount  int64
	SilentRuns   int64
	StartNs      int64
	EndNs        int64
	RangeSet     bool
}

// AudioPoint is one audio block's timing and payload used for statistics.
type AudioPoint struct {
	TrackNumber uint32
	StartNs     int64
	EndNs       int64
	Payload     []byte
}

// SummarizeAudio groups audio blocks by track, counts samples, tracks the
// time range and counts maximal all-zero (silent) runs. It never decodes.
func SummarizeAudio(points []AudioPoint) []AudioStats {
	byTrack := map[uint32]*AudioStats{}
	var order []uint32
	silent := map[uint32]bool{}
	for _, p := range points {
		st, ok := byTrack[p.TrackNumber]
		if !ok {
			st = &AudioStats{TrackNumber: p.TrackNumber}
			byTrack[p.TrackNumber] = st
			order = append(order, p.TrackNumber)
		}
		st.SampleCount += int64(len(p.Payload))
		if !st.RangeSet || p.StartNs < st.StartNs {
			st.StartNs = p.StartNs
			st.RangeSet = true
		}
		if p.EndNs > st.EndNs {
			st.EndNs = p.EndNs
		}
		zero := len(p.Payload) > 0 && allZero(p.Payload)
		// Count a silent run on the transition non-silent -> silent.
		if zero && !silent[p.TrackNumber] {
			st.SilentRuns++
		}
		silent[p.TrackNumber] = zero
	}
	out := make([]AudioStats, 0, len(order))
	for _, n := range order {
		out = append(out, *byTrack[n])
	}
	return out
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
