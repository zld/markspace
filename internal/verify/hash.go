package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"mediasync/internal/model"
)

// SegmentHash is the deterministic digest of one output segment. Only stable
// fields participate: identity, timing on the shared clock and payload. Map
// iteration and pointer addresses never enter the hash, so identical inputs
// produce identical hashes across runs and machines.
type SegmentHash struct {
	Representation string
	PeriodID       int
	Index          int
	SHA256         string
}

// HashSegment computes the SHA-256 digest of a segment's canonical encoding.
func HashSegment(s model.SegmentDescriptor) SegmentHash {
	h := sha256.New()
	writeField := func(label string, b []byte) {
		h.Write([]byte(label))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{'|'})
	}
	writeField("src", []byte(s.Source))
	writeField("rep", []byte(s.Representation))
	writeField("period", itoa(int64(s.PeriodID)))
	writeField("index", itoa(int64(s.Index)))
	writeField("kind", []byte(s.Kind.String()))
	writeField("start", itoa(s.StartNS))
	writeField("duration", itoa(s.DurationNS))
	writeField("keyframe", boolBytes(s.Keyframe))
	writeField("payload", s.Payload)
	return SegmentHash{
		Representation: s.Representation, PeriodID: s.PeriodID, Index: s.Index,
		SHA256: hex.EncodeToString(h.Sum(nil)),
	}
}

// HashAll hashes every segment and returns results in a deterministic order:
// period, representation, index. The input slice is not mutated.
func HashAll(segs []model.SegmentDescriptor) []SegmentHash {
	out := make([]SegmentHash, 0, len(segs))
	for _, s := range segs {
		out = append(out, HashSegment(s))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].PeriodID != out[j].PeriodID {
			return out[i].PeriodID < out[j].PeriodID
		}
		if out[i].Representation != out[j].Representation {
			return out[i].Representation < out[j].Representation
		}
		return out[i].Index < out[j].Index
	})
	return out
}

// HashSet is an order-independent fingerprint of a whole segment collection.
// Individual segment hashes are sorted before digesting so the same set hashes
// identically regardless of processing order.
func HashSet(segs []model.SegmentDescriptor) string {
	hashes := HashAll(segs)
	digests := make([]string, len(hashes))
	for i, h := range hashes {
		digests[i] = h.SHA256
	}
	sort.Strings(digests)
	h := sha256.New()
	for _, d := range digests {
		h.Write([]byte(d))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func itoa(v int64) []byte {
	if v == 0 {
		return []byte{'0'}
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return buf[i:]
}

func boolBytes(b bool) []byte {
	if b {
		return []byte{'1'}
	}
	return []byte{'0'}
}
