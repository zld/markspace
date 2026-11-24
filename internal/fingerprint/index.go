// Package fingerprint builds content fingerprints for segmented media and
// detects duplicate content. Only local files are read; the resulting index
// is written back to a local file.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"sort"

	"mediamerge/internal/model"
	"mediamerge/internal/segment"
)

// Part is the per-sample digest kept for collision verification.
type Part struct {
	TrackID uint32 `json:"track_id"`
	Size    uint32 `json:"size"`
	Digest  string `json:"digest"`
}

// Fingerprint is the content identity of one segment. Timestamps and source
// paths are deliberately excluded: identical content only shifted in time
// must still match.
type Fingerprint struct {
	Segment    int      `json:"segment"`
	Tracks     []uint32 `json:"tracks"`
	TotalBytes int64    `json:"total_bytes"`
	Quick      uint32   `json:"quick"`
	Strong     string   `json:"strong_sha256"`
	Parts      []Part   `json:"parts"`
	Partial    bool     `json:"partial"` // byte cap hit: never deduplicated
	Empty      bool     `json:"empty"`   // no payload: never deduplicated
}

// Duplicate records one dropped segment and the kept original.
type Duplicate struct {
	Kept     int    `json:"kept_segment"`
	Dropped  int    `json:"dropped_segment"`
	Bytes    int64  `json:"bytes"`
	Verified bool   `json:"sample_verified"`
	FromHash string `json:"strong_sha256"`
}

// Collision is a quick-hash candidate that per-sample verification proved
// distinct. Both segments are kept.
type Collision struct {
	SegmentA int    `json:"segment_a"`
	SegmentB int    `json:"segment_b"`
	Quick    uint32 `json:"quick"`
	Detail   string `json:"detail"`
}

// Report is the dedupe result for a whole run.
type Report struct {
	Fingerprints []Fingerprint `json:"fingerprints"`
	Duplicates   []Duplicate   `json:"duplicates"`
	Collisions   []Collision   `json:"collisions"`
}

// Options bounds fingerprint memory use and allows the weak hash to be
// replaced (tests use this to force collision candidates deterministically).
type Options struct {
	MaxTotalBytes int64
	MaxPartBytes  int
	WeakHash      func(trackID uint32, data []byte, seed uint32) uint32
}

func (o Options) maxTotal() int64 {
	if o.MaxTotalBytes > 0 {
		return o.MaxTotalBytes
	}
	return 16 << 20
}

func (o Options) maxPart() int {
	if o.MaxPartBytes > 0 {
		return o.MaxPartBytes
	}
	return 1 << 20
}

func (o Options) weak() func(uint32, []byte, uint32) uint32 {
	if o.WeakHash != nil {
		return o.WeakHash
	}
	return func(_ uint32, data []byte, seed uint32) uint32 {
		h := fnv.New32a()
		var s [4]byte
		s[0], s[1], s[2], s[3] = byte(seed), byte(seed>>8), byte(seed>>16), byte(seed>>24)
		_, _ = h.Write(s[:])
		_, _ = h.Write(data)
		return h.Sum32()
	}
}

// PayloadFunc returns the payload bytes of a sample; false means unreadable.
type PayloadFunc func(s model.Sample) ([]byte, bool)

// Build computes fingerprints for the given segments using only samples of
// the selected tracks (empty trackIDs means every track).
func Build(segments []segment.Segment, trackIDs []uint32, payload PayloadFunc, opts Options) Report {
	want := make(map[uint32]bool, len(trackIDs))
	for _, id := range trackIDs {
		want[id] = true
	}
	weak := opts.weak()
	rep := Report{}
	for _, seg := range segments {
		samples := append([]model.Sample(nil), seg.Samples...)
		sort.SliceStable(samples, func(i, j int) bool { return samples[i].Key() < samples[j].Key() })
		fp := Fingerprint{Segment: seg.Index}
		trackSet := make(map[uint32]bool)
		var total int64
		strong := sha256.New()
		var seed uint32
		stop := false
		for _, s := range samples {
			if len(want) > 0 && !want[s.TrackID] {
				continue
			}
			if stop {
				fp.Partial = true
				break
			}
			data, ok := payload(s)
			if !ok {
				fp.Partial = true // unreadable payload cannot be fingerprinted
				stop = true
				continue
			}
			if len(data) > opts.maxPart() {
				data = data[:opts.maxPart()]
				fp.Partial = true
			}
			if total+int64(len(data)) > opts.maxTotal() {
				fp.Partial = true
				stop = true
				continue
			}
			trackSet[s.TrackID] = true
			total += int64(len(data))
			seed = mix(seed, weak(s.TrackID, data, seed), s.Size)
			sum := sha256.Sum256(data)
			strong.Write(sum[:])
			var sz [4]byte
			sz[0], sz[1], sz[2], sz[3] = byte(s.Size), byte(s.Size>>8), byte(s.Size>>16), byte(s.Size>>24)
			strong.Write(sz[:])
			fp.Parts = append(fp.Parts, Part{TrackID: s.TrackID, Size: s.Size,
				Digest: hex.EncodeToString(sum[:])})
		}
		for id := range trackSet {
			fp.Tracks = append(fp.Tracks, id)
		}
		sort.Slice(fp.Tracks, func(i, j int) bool { return fp.Tracks[i] < fp.Tracks[j] })
		fp.TotalBytes = total
		fp.Quick = seed
		fp.Strong = hex.EncodeToString(strong.Sum(nil))
		fp.Empty = len(fp.Parts) == 0
		rep.Fingerprints = append(rep.Fingerprints, fp)
	}
	rep.Duplicates, rep.Collisions = dedupe(rep.Fingerprints)
	return rep
}

func mix(seed, weak uint32, size uint32) uint32 {
	x := seed ^ weak
	x *= 16777619
	x ^= size
	return x
}

type fingerprintGroup struct {
	key   string
	first int
	rest  []int
}

func dedupe(fps []Fingerprint) ([]Duplicate, []Collision) {
	groups := make(map[string]*fingerprintGroup)
	var order []string
	for i, fp := range fps {
		if fp.Empty || fp.Partial {
			continue
		}
		key := fmt.Sprintf("%d/%d/%s", fp.Quick, fp.TotalBytes, trackSig(fp.Tracks))
		g, ok := groups[key]
		if !ok {
			g = &fingerprintGroup{key: key, first: i}
			groups[key] = g
			order = append(order, key)
			continue
		}
		g.rest = append(g.rest, i)
	}

	var dups []Duplicate
	var collisions []Collision
	for _, key := range order {
		g := groups[key]
		kept := fps[g.first]
		for _, idx := range g.rest {
			cand := fps[idx]
			switch {
			case cand.Strong == kept.Strong:
				dups = append(dups, Duplicate{Kept: kept.Segment, Dropped: cand.Segment,
					Bytes: cand.TotalBytes, Verified: true, FromHash: cand.Strong})
			case sameSamples(kept.Parts, cand.Parts):
				dups = append(dups, Duplicate{Kept: kept.Segment, Dropped: cand.Segment,
					Bytes: cand.TotalBytes, Verified: true, FromHash: cand.Strong})
			default:
				// Quick hashes agree but content differs: candidate rejected
				// only after comparing every sample digest.
				collisions = append(collisions, Collision{SegmentA: kept.Segment,
					SegmentB: cand.Segment, Quick: cand.Quick,
					Detail: fmt.Sprintf("quick hash %08x matched %d parts; per-sample digests differ",
						cand.Quick, len(cand.Parts))})
			}
		}
	}
	sort.Slice(dups, func(i, j int) bool { return dups[i].Dropped < dups[j].Dropped })
	sort.Slice(collisions, func(i, j int) bool {
		if collisions[i].SegmentA != collisions[j].SegmentA {
			return collisions[i].SegmentA < collisions[j].SegmentA
		}
		return collisions[i].SegmentB < collisions[j].SegmentB
	})
	return dups, collisions
}

// sameSamples is the per-sample verification step for collision candidates.
func sameSamples(a, b []Part) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].TrackID != b[i].TrackID || a[i].Size != b[i].Size || a[i].Digest != b[i].Digest {
			return false
		}
	}
	return true
}

func trackSig(ids []uint32) string {
	b := make([]byte, 0, len(ids)*4)
	for _, id := range ids {
		b = append(b, byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
	}
	return string(b)
}

// WriteFile persists the report as canonical JSON to a local path only.
func (r Report) WriteFile(path string) error {
	if path == "" {
		return fmt.Errorf("fingerprint: empty index path")
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}
