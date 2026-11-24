package fingerprint

import (
	"encoding/binary"
	"hash"
	"hash/fnv"

	"mediapipe/internal/audio"
	"mediapipe/internal/model"
)

// HashedWindow contains a fingerprint and the hasher state used while adding
// ordered packets.
type HashedWindow struct {
	model.WindowFingerprint
	hasher hash.Hash64
}

// HashWindows computes deterministic FNV-1a fingerprints for fixed-length
// content windows. Absolute stream time and RTP sequence numbers are not
// hashed so the same content at another source position can be recognized;
// relative timing is compared separately under the configured tolerance.
func HashWindows(segment model.Segment, packets []model.Packet, windowMs int64) []model.WindowFingerprint {
	if windowMs <= 0 {
		windowMs = 200
	}
	duration := segment.EndMs - segment.StartMs
	count := int(duration / windowMs)
	if count <= 0 {
		return nil
	}

	type acc struct {
		fp         model.WindowFingerprint
		hasher     hash.Hash64
		coverageMs int64
		bad        bool
	}
	windows := make([]acc, count)
	for i := range windows {
		start := segment.StartMs + int64(i)*windowMs
		h := fnv.New64a()
		windows[i] = acc{
			fp:     model.WindowFingerprint{WindowIndex: i, StartMs: start, EndMs: start + windowMs},
			hasher: h,
		}
	}

	for _, p := range packets {
		frameMs := p.FrameMs
		if frameMs <= 0 {
			frameMs = 20
		}
		for i := range windows {
			w := &windows[i]
			overlapStart := max64(p.MediaMs, w.fp.StartMs)
			overlapEnd := min64(p.MediaMs+frameMs, w.fp.EndMs)
			if overlapEnd <= overlapStart {
				continue
			}
			w.coverageMs += overlapEnd - overlapStart
			if !p.PayloadValid {
				w.bad = true
				continue
			}
			if _, _, err := audio.DecodePayload(p.Payload); err != nil {
				w.bad = true
				continue
			}
			var timing [8]byte
			binary.LittleEndian.PutUint64(timing[:], uint64(p.MediaMs-segment.StartMs))
			_, _ = w.hasher.Write(timing[:])
			_, _ = w.hasher.Write(p.Payload)
			w.fp.SampleCount += len(p.Payload) / 2
		}
	}

	out := make([]model.WindowFingerprint, 0, count)
	for i := range windows {
		w := &windows[i]
		w.fp.SegmentIndex = segment.Index
		w.fp.Hash = w.hasher.Sum64()
		w.fp.Complete = !w.bad && w.coverageMs >= windowMs && w.fp.SampleCount > 0
		out = append(out, w.fp)
	}
	return out
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
