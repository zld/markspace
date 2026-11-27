// Package fingerprint derives stable content fingerprints from normalized
// tracks and their recovered time boundaries. Fingerprinting only runs over
// trusted boundaries; untrusted material is flagged for review instead of
// being merged automatically.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"

	"mediarc/internal/model"
)

// NormalizedTrack is the canonical fingerprint input: codec identity, the
// time basis, and every sample in decode order with its exact bytes.
type NormalizedTrack struct {
	Role      string
	Codec     model.Codec
	Timescale uint32
	Samples   []model.Sample
}

// BoundaryFingerprint covers the recovered segment boundaries on the shared
// millisecond timeline.
type BoundaryFingerprint struct {
	Starts []int64
	Ends   []int64
}

// Digest is one deterministic content fingerprint.
type Digest struct {
	// SampleHash covers codec, timescale and the compressed sample bytes in
	// order, so any byte or ordering change changes the fingerprint.
	SampleHash string
	// BoundaryHash covers the normalized shared-timeline boundaries.
	BoundaryHash string
	// Trusted is false when any participating track reported untrusted
	// boundaries; such digests must never drive automatic dedup.
	Trusted bool
}

// Normalize converts a recovered representation's tracks into the canonical
// fingerprint shape. Roles order the tracks deterministically regardless of
// container track id or source representation.
func Normalize(roles map[uint32]string, rep model.RecoveredRepresentation) ([]NormalizedTrack, bool) {
	trusted := true
	out := make([]NormalizedTrack, 0, len(rep.Tracks))
	for i := range rep.Tracks {
		rt := rep.Tracks[i]
		if !rt.Trusted {
			trusted = false
		}
		role := roles[rt.Track.ID]
		if role == "" {
			role = string(rt.Track.Codec) + ":" + strconv.FormatUint(uint64(rt.Track.ID), 10)
		}
		samples := make([]model.Sample, len(rt.Track.Samples))
		copy(samples, rt.Track.Samples)
		out = append(out, NormalizedTrack{
			Role: role, Codec: rt.Track.Codec, Timescale: rt.Track.Timescale, Samples: samples,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Role < out[j].Role })
	return out, trusted
}

// Hash builds the deterministic digest. Boundaries come from the shared
// fan-in timeline and are included so two byte-identical media items with
// different segmentation never collapse into one archive entry.
func Hash(tracks []NormalizedTrack, boundaries []model.Boundary, trusted bool) Digest {
	h := sha256.New()
	for _, tr := range tracks {
		writeLine(h, "role="+tr.Role)
		writeLine(h, "codec="+string(tr.Codec))
		writeLine(h, "timescale="+strconv.FormatUint(uint64(tr.Timescale), 10))
		writeLine(h, "samples="+strconv.Itoa(len(tr.Samples)))
		for i, s := range tr.Samples {
			writeLine(h, strconv.Itoa(i)+":"+strconv.FormatUint(s.Dts, 10)+":"+
				strconv.FormatUint(uint64(s.Duration), 10)+":"+strconv.FormatBool(s.Sync))
			h.Write(s.Payload)
			h.Write([]byte{'\n'})
		}
	}
	bf := sha256.New()
	for _, b := range boundaries {
		writeLine(bf, strconv.Itoa(b.SegmentIndex)+":"+
			strconv.FormatInt(b.StartMs, 10)+":"+strconv.FormatInt(b.EndMs, 10))
	}
	return Digest{
		SampleHash:   hex.EncodeToString(h.Sum(nil)),
		BoundaryHash: hex.EncodeToString(bf.Sum(nil)),
		Trusted:      trusted,
	}
}

// Equal reports whether two digests cover identical samples and boundaries.
func (d Digest) Equal(other Digest) bool {
	return d.SampleHash == other.SampleHash && d.BoundaryHash == other.BoundaryHash
}

// DedupGroup clusters representations that share a trusted digest.
type DedupGroup struct {
	SampleHash   string
	BoundaryHash string
	Members      []string
}

// ReviewItem is material whose boundaries cannot be trusted; it is held for
// manual review instead of being merged.
type ReviewItem struct {
	RepID  string
	Reason string
}

// Dedup partitions recovered representations. Trusted, identical digests form
// one group; untrusted items are returned separately for review.
func Dedup(repIDs []string, digests map[string]Digest) (groups []DedupGroup, review []ReviewItem) {
	idx := map[string]int{}
	for _, id := range repIDs {
		d, ok := digests[id]
		if !ok {
			review = append(review, ReviewItem{RepID: id, Reason: "no fingerprint produced"})
			continue
		}
		if !d.Trusted {
			review = append(review, ReviewItem{RepID: id, Reason: "untrusted segment boundaries"})
			continue
		}
		key := d.SampleHash + "|" + d.BoundaryHash
		if g, exists := idx[key]; exists {
			groups[g].Members = append(groups[g].Members, id)
			continue
		}
		idx[key] = len(groups)
		groups = append(groups, DedupGroup{
			SampleHash: d.SampleHash, BoundaryHash: d.BoundaryHash, Members: []string{id},
		})
	}
	return groups, review
}

func writeLine(h interface{ Write([]byte) (int, error) }, s string) {
	_, _ = h.Write([]byte(s))
	_, _ = h.Write([]byte{'\n'})
}
