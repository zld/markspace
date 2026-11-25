// Package fingerprint produces stable, offset-independent content hashes for
// split segments and groups duplicates. Two segments carrying identical media
// content map to one fingerprint even when their input byte offsets differ;
// candidates whose hash collides but whose content differs stay independent.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"

	"mediad/internal/mp4"
)

// Fingerprint is the stable identity of one piece of content.
type Fingerprint struct {
	Hash      string `json:"hash"`
	FirstSeen int64  `json:"first_seen_offset"` // input offset of the canonical copy
}

// ContentHash hashes normalised media bytes. Location-dependent framing
// (trun data offsets) must already be removed by the caller via Normalise.
func ContentHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// SegmentPayload extracts the concatenated sample bytes of one track fragment
// in sample order. Hashing the media payload (not the box headers) makes the
// fingerprint independent of where the fragment sat in the input.
func SegmentPayload(data []byte, tf *mp4.TrackFragment) []byte {
	out := make([]byte, 0)
	for _, s := range tf.Samples {
		if s.DataOffset < 0 || s.DataOffset+s.Size > int64(len(data)) {
			continue
		}
		out = append(out, data[s.DataOffset:s.DataOffset+s.Size]...)
	}
	return out
}

// HashFragment returns the media-content fingerprint of a track fragment.
func HashFragment(data []byte, tf *mp4.TrackFragment) string {
	return ContentHash(SegmentPayload(data, tf))
}

// Candidate is a content item offered to the deduper.
type Candidate struct {
	// Key is the content hash; identical content yields an identical key.
	Key string
	// ContentOK lets a caller that already detected a hash collision with
	// differing bytes force the candidate to remain a separate entry.
	ContentOK bool
	Offset    int64
}

// Dedup maps content hashes to a canonical fingerprint while preserving
// colliding-but-different candidates as independent entries.
type Dedup struct {
	byHash map[string]*Fingerprint
	// conflicts holds synthetic distinct hashes for collision candidates.
	conflicts map[string]int
}

// NewDedup builds an empty deduper.
func NewDedup() *Dedup {
	return &Dedup{byHash: map[string]*Fingerprint{}, conflicts: map[string]int{}}
}

// Add registers a candidate and returns the fingerprint it points to. Equal
// content at a different offset returns the existing fingerprint; a flagged
// collision candidate is kept independent under a derived hash.
func (d *Dedup) Add(c Candidate) Fingerprint {
	if !c.ContentOK {
		// Preserve as independent content even though the raw hash collided.
		n := d.conflicts[c.Key]
		d.conflicts[c.Key] = n + 1
		derived := c.Key + "::conflict" + strconv.Itoa(n)
		fp := &Fingerprint{Hash: derived, FirstSeen: c.Offset}
		d.byHash[derived] = fp
		return *fp
	}
	if fp, ok := d.byHash[c.Key]; ok {
		return *fp
	}
	fp := &Fingerprint{Hash: c.Key, FirstSeen: c.Offset}
	d.byHash[c.Key] = fp
	return *fp
}

// Fingerprints returns all canonical fingerprints, sorted for determinism.
func (d *Dedup) Fingerprints() []Fingerprint {
	out := make([]Fingerprint, 0, len(d.byHash))
	for _, fp := range d.byHash {
		out = append(out, *fp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hash < out[j].Hash })
	return out
}
