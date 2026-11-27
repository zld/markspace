package timeline

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// Checkpoint is a restorable prefix of the committed timeline.
type Checkpoint struct {
	WatermarkNS int64  `json:"watermark_ns"`
	ItemCount   int    `json:"item_count"`
	ReplayCount int    `json:"replay_count"`
	DigestState []byte `json:"digest_state"`
}

// Watermark owns the committed region. Items at or below WatermarkNS are
// committed; anything older arriving after a raise is late and triggers a
// rollback-recompute from the nearest checkpoint.
type Watermark struct {
	committed   []Item
	wmNS        int64
	checkpoints []Checkpoint
	replays     int
	digest      [sha256.Size]byte
}

// NewWatermark starts at the epoch with one root checkpoint.
func NewWatermark() *Watermark {
	w := &Watermark{}
	w.checkpoints = append(w.checkpoints, Checkpoint{
		WatermarkNS: 0,
		DigestState: w.digest[:],
	})
	return w
}

// WatermarkNS reports the current high-water mark.
func (w *Watermark) WatermarkNS() int64 { return w.wmNS }

// Committed returns a copy of committed items sorted by time.
func (w *Watermark) Committed() []Item {
	out := make([]Item, len(w.committed))
	copy(out, w.committed)
	return out
}

// Advance raises the watermark to ns, commits every pending item at or below
// it (sorted stably by time), and returns the newly committed items.
func (w *Watermark) Advance(ns int64, pending []Item) []Item {
	if ns < w.wmNS {
		ns = w.wmNS
	}
	ready := make([]Item, 0, len(pending))
	for _, it := range pending {
		if it.NS <= ns {
			ready = append(ready, it)
		}
	}
	sort.SliceStable(ready, func(i, j int) bool { return ready[i].NS < ready[j].NS })
	for _, it := range ready {
		w.foldDigest(it)
	}
	w.committed = append(w.committed, ready...)
	w.wmNS = ns
	return ready
}

// Snapshot stores a checkpoint at the current watermark.
func (w *Watermark) Snapshot() Checkpoint {
	cp := Checkpoint{
		WatermarkNS: w.wmNS,
		ItemCount:   len(w.committed),
		ReplayCount: w.replays,
		DigestState: append([]byte(nil), w.digest[:]...),
	}
	w.checkpoints = append(w.checkpoints, cp)
	return cp
}

// Late returns true when an item's time is below the current watermark.
func (w *Watermark) Late(ns int64) bool { return ns < w.wmNS }

// Replay rolls back to the nearest checkpoint at or before ns, re-applies all
// surviving committed items plus the late item in event-time order, and
// rebuilds to the prior watermark. Items strictly later than the rollback
// point are recomputed; the caller must re-supply any external conclusions.
func (w *Watermark) Replay(late Item) (rollbackFromNS int64, replayed []Item) {
	idx := len(w.checkpoints) - 1
	for idx > 0 && w.checkpoints[idx].WatermarkNS > late.NS {
		idx--
	}
	cp := w.checkpoints[idx]
	rollbackFromNS = w.wmNS

	w.digest = [sha256.Size]byte{}
	copy(w.digest[:], cp.DigestState)
	kept := append([]Item(nil), w.committed[:cp.ItemCount]...)
	survivors := append([]Item(nil), w.committed[cp.ItemCount:]...)
	survivors = append(survivors, late)
	// Dedup by stable identity so replaying the same late datum is a no-op.
	identities := map[string]struct{}{}
	for _, it := range kept {
		identities[it.Identity()] = struct{}{}
	}
	unique := survivors[:0]
	for _, it := range survivors {
		id := it.Identity()
		if _, ok := identities[id]; ok {
			continue
		}
		identities[id] = struct{}{}
		unique = append(unique, it)
	}
	sort.SliceStable(unique, func(i, j int) bool { return unique[i].NS < unique[j].NS })
	for _, it := range unique {
		w.foldDigest(it)
	}
	w.committed = append(kept, unique...)
	w.replays++
	return rollbackFromNS, unique
}

// ReplayCount reports how many late-event recomputes happened.
func (w *Watermark) ReplayCount() int { return w.replays }

// Summary finalizes the deterministic digest over the committed prefix.
func (w *Watermark) Summary(gaps []Gap) Summary {
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].Source != gaps[j].Source {
			return gaps[i].Source < gaps[j].Source
		}
		return gaps[i].FromSequence < gaps[j].FromSequence
	})
	return Summary{
		WatermarkNS: w.wmNS,
		ItemCount:   len(w.committed),
		GapCount:    len(gaps),
		ReplayCount: w.replays,
		Digest:      hex.EncodeToString(w.digest[:]),
		Gaps:        gaps,
	}
}

func (w *Watermark) foldDigest(it Item) {
	h := sha256.New()
	h.Write(w.digest[:])
	writeDigestField(h, it.Kind)
	writeDigestField(h, it.Source)
	writeDigestField(h, intToBytes(it.NS))
	writeDigestField(h, intToBytes(it.EndNS))
	writeDigestField(h, uintToBytes(it.Sequence))
	writeDigestField(h, it.Key)
	if len(it.Digest) > 0 {
		h.Write(it.Digest)
	}
	w.digest = [sha256.Size]byte{}
	copy(w.digest[:], h.Sum(nil))
}

func writeDigestField(h interface{ Write([]byte) (int, error) }, v interface{}) {
	switch x := v.(type) {
	case string:
		h.Write([]byte(x))
	case []byte:
		h.Write(x)
	}
	h.Write([]byte{0})
}

func intToBytes(v int64) []byte {
	return uintToBytes(uint64(v))
}

func uintToBytes(v uint64) []byte {
	b := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	return b
}
