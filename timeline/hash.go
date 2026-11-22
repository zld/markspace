package timeline

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"

	"mediacore"
)

// SegmentHasher builds canonical, deterministic SHA-256 fingerprints of
// segments. Hashing covers the full input event range, every recovery action
// and the assigned index offset, so any change in a recomputation decision is
// visible while unaffected segments hash identically across runs.
type SegmentHasher struct{}

// NewSegmentHasher creates the stateless shared hasher.
func NewSegmentHasher() *SegmentHasher { return &SegmentHasher{} }

// Hash computes the SHA-256 of a segment's canonical encoding.
func (h *SegmentHasher) Hash(seg *Segment) []byte {
	sum := sha256.New()
	writeInt64(sum, seg.StartNS)
	writeInt64(sum, seg.EndNS)
	writeInt64(sum, seg.InputFrom)
	writeInt64(sum, seg.InputTo)
	writeUint64(sum, uint64(seg.IndexOff))

	// Events ordered by (track, time, seq) so arrival order cannot perturb
	// the hash.
	evs := append([]mediacore.Event(nil), seg.Events...)
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].Track != evs[j].Track {
			return evs[i].Track < evs[j].Track
		}
		if evs[i].Time != evs[j].Time {
			return evs[i].Time < evs[j].Time
		}
		return evs[i].Seq < evs[j].Seq
	})
	writeUint64(sum, uint64(len(evs)))
	for _, ev := range evs {
		sum.Write([]byte(ev.Track))
		sum.Write([]byte{byte(ev.Kind)})
		writeInt64(sum, ev.Time)
		writeInt64(sum, ev.OrigTime)
		writeInt64(sum, ev.Recv)
		writeUint64(sum, ev.Seq)
		writeUint32(sum, ev.RTPTS)
		writeUint64(sum, uint64(ev.Samples))
		writeUint64(sum, uint64(ev.Channels))
		writeUint64(sum, uint64(ev.Rate))
		if ev.Keyframe {
			sum.Write([]byte{1})
		} else {
			sum.Write([]byte{0})
		}
		sum.Write(ev.Payload)
		sum.Write([]byte(ev.Reason))
	}

	// Actions sorted to a canonical order before hashing.
	acts := append([]Action(nil), seg.Actions...)
	sort.SliceStable(acts, func(i, j int) bool {
		if acts[i].Track != acts[j].Track {
			return acts[i].Track < acts[j].Track
		}
		if acts[i].Offset != acts[j].Offset {
			return acts[i].Offset < acts[j].Offset
		}
		return acts[i].Kind < acts[j].Kind
	})
	writeUint64(sum, uint64(len(acts)))
	for _, a := range acts {
		sum.Write([]byte(a.Kind))
		sum.Write([]byte(a.Track))
		writeInt64(sum, a.Offset)
		sum.Write([]byte(a.Detail))
	}
	return sum.Sum(nil)
}

// HashHex returns the lowercase hex form of Hash.
func (h *SegmentHasher) HashHex(seg *Segment) string { return hex.EncodeToString(h.Hash(seg)) }

func writeInt64(h interface{ Write([]byte) (int, error) }, v int64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	h.Write(b[:])
}

func writeUint64(h interface{ Write([]byte) (int, error) }, v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	h.Write(b[:])
}

func writeUint32(h interface{ Write([]byte) (int, error) }, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	h.Write(b[:])
}
