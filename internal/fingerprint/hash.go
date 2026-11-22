// Package fingerprint content-addresses media segments so duplicate camera
// feeds can be collapsed without comparing bytes blindly.
//
// A fingerprint combines a normalised time extent with a SHA-256 over the
// segment's sample payload. Segments with equal content hashes but different
// normalised extents are never merged silently: the caller must re-verify the
// samples one by one, because an equal hash over different spans is either a
// truncated read or a genuine collision.
package fingerprint

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/pairwise/mediad/internal/fault"
)

// Fingerprint identifies a segment's normalized extent and content.
type Fingerprint struct {
	StartNS     int64 // normalized media start on the unified timeline
	EndNS       int64 // normalized media end
	TrackID     uint32
	SampleCount int
	Content     [32]byte
	// ContentHex is the full SHA-256 of the segment bytes, rendered as hex.
	ContentHex string
	// Key is the deduplication key: extent plus content digest.
	Key string
}

// Samples is a readable sample provider. ReaderAt returns the sample payload
// at its absolute file offset.
type Samples interface {
	ReadSample(trackID uint32, index int) ([]byte, error)
	Count(trackID uint32) int
}

// Sum streams the given sample indices and returns the digest.
func Sum(src Samples, trackID uint32, first, count int) ([32]byte, [][32]byte, error) {
	if count < 0 {
		return [32]byte{}, nil, fault.Errorf("fingerprint.sum", fault.KindFingerprint, "negative sample count %d", count).WithTrack(trackID)
	}
	if first < 0 || first+count > src.Count(trackID) {
		return [32]byte{}, nil, fault.Errorf("fingerprint.sum", fault.KindFingerprint,
			"sample range [%d,%d) outside track %d with %d samples", first, first+count, trackID, src.Count(trackID)).
			WithTrack(trackID).Wrapped(fault.ErrBadSample)
	}
	h := sha256.New()
	per := make([][32]byte, 0, count)
	var lenbuf [4]byte
	for i := first; i < first+count; i++ {
		payload, err := src.ReadSample(trackID, i)
		if err != nil {
			return [32]byte{}, nil, fault.New("fingerprint.sum", fault.KindFingerprint, err).
				WithTrack(trackID).WithSample(i)
		}
		binary.BigEndian.PutUint32(lenbuf[:], uint32(len(payload)))
		h.Write(lenbuf[:])
		h.Write(payload)
		per = append(per, sha256.Sum256(payload))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, per, nil
}

// New builds a fingerprint for [startNS,endNS) over count samples.
func New(trackID uint32, startNS, endNS int64, count int, content [32]byte) Fingerprint {
	fp := Fingerprint{
		StartNS: startNS, EndNS: endNS, TrackID: trackID,
		SampleCount: count, Content: content,
	}
	fp.ContentHex = hex.EncodeToString(content[:])
	fp.Key = fmt.Sprintf("%d|%d|%d|%s", startNS, endNS, trackID, fp.ContentHex)
	return fp
}

// Segment is a candidate produced upstream.
type Segment struct {
	TrackID     uint32
	StartNS     int64
	EndNS       int64
	FirstSample int
	SampleCount int
	Content     [32]byte
	PerSample   [][32]byte
}

// Decision explains how a candidate was classified.
type Decision struct {
	Segment   Segment
	Duplicate bool
	Of        int  // index of the earlier segment it duplicates, -1 if unique
	Review    bool // hash matched across differing extents; samples checked
	Reason    string
}

// dedupKey merges only identical normalized extents.
type extent struct {
	track      uint32
	start, end int64
	content    [32]byte
}

// Dedupe classifies segments in input order. Two segments are duplicates only
// when their normalized extents and content digests are identical. Matching
// digests over different extents trigger per-sample review; if every sample
// digest also matches, the later segment is a duplicate with Review set,
// otherwise it is kept as unique with the collision recorded as the reason.
func Dedupe(segments []Segment) ([]Decision, error) {
	seenExtent := map[extent]int{}
	byContent := map[[32]byte][]int{}
	out := make([]Decision, len(segments))
	for i, s := range segments {
		if s.EndNS <= s.StartNS {
			return nil, fault.Errorf("fingerprint.dedupe", fault.KindFingerprint,
				"segment %d has empty extent [%d,%d)", i, s.StartNS, s.EndNS).WithTrack(s.TrackID)
		}
		key := extent{track: s.TrackID, start: s.StartNS, end: s.EndNS, content: s.Content}
		d := Decision{Segment: s, Of: -1}
		if j, ok := seenExtent[key]; ok {
			d.Duplicate, d.Of, d.Reason = true, j, "identical extents and content"
			out[i] = d
			continue
		}
		earlier := byContent[s.Content]
		if len(earlier) > 0 {
			j := earlier[0]
			prev := segments[j]
			ok, err := samplesMatch(prev, s)
			if err != nil {
				return nil, err
			}
			if ok {
				d.Duplicate, d.Of, d.Review = true, j, true
				d.Reason = "content hash matched across extents; per-sample review confirmed"
				seenExtent[key] = j
				out[i] = d
				continue
			}
			d.Review = true
			d.Reason = "hash collision: extents differ and sample digests disagree"
			out[i] = d
			byContent[s.Content] = append(byContent[s.Content], i)
			continue
		}
		seenExtent[key] = i
		byContent[s.Content] = append(byContent[s.Content], i)
		out[i] = d
	}
	return out, nil
}

func samplesMatch(a, b Segment) (bool, error) {
	if a.SampleCount != b.SampleCount {
		return false, nil
	}
	if len(a.PerSample) != len(b.PerSample) {
		return false, fault.Errorf("fingerprint.review", fault.KindFingerprint,
			"per-sample digests unavailable for collision review").WithTrack(a.TrackID)
	}
	for i := range a.PerSample {
		if a.PerSample[i] != b.PerSample[i] {
			return false, nil
		}
	}
	return true, nil
}

// ManifestHash hashes a canonical rendering of segment fingerprints. It is
// the value compared between a continuous run and a resumed run.
func ManifestHash(fps []Fingerprint) string {
	cp := append([]Fingerprint(nil), fps...)
	sort.Slice(cp, func(i, j int) bool {
		if cp[i].StartNS != cp[j].StartNS {
			return cp[i].StartNS < cp[j].StartNS
		}
		if cp[i].EndNS != cp[j].EndNS {
			return cp[i].EndNS < cp[j].EndNS
		}
		return cp[i].TrackID < cp[j].TrackID
	})
	h := sha256.New()
	for _, fp := range cp {
		fmt.Fprintf(h, "%d|%d|%d|%d|%s\n", fp.TrackID, fp.StartNS, fp.EndNS, fp.SampleCount, fp.ContentHex)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ErrCollision is returned by ResolveCollision when review cannot separate
// two segments and the caller asked for strict handling.
var ErrCollision = errors.New("fingerprint collision")

// ResolveCollision re-checks two segments sample by sample. It returns true
// when they are provably the same content and an error when the evidence is
// insufficient; it never merges on hash equality alone.
func ResolveCollision(a, b Segment) (bool, error) {
	if a.Content != b.Content {
		return false, nil
	}
	if len(a.PerSample) == 0 && len(b.PerSample) == 0 {
		return false, ErrCollision
	}
	ok, err := samplesMatch(a, b)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return true, nil
}
