// Package fingerprint identifies segment content and decides duplicates.
//
// A fingerprint is a digest over the coded sample payloads only: container
// framing, offsets and fragment ordinals are deliberately excluded so a
// re-packaged fragment of the same media still matches. Hits are never trusted
// on their own — a hit must survive duration, sampled-byte and full-content
// verification before content may be dropped.
package fingerprint

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	"avbatch/internal/mediaerr"
)

// Config controls fingerprint strength and duplicate tolerance.
type Config struct {
	// ShortBytes is how many leading digest bytes form the lookup key.
	// Smaller values collide more often, which is exactly what the collision
	// recheck path has to survive.
	ShortBytes int
	// DurationToleranceNanos is how far two durations may differ and still be
	// considered the same content. Zero requires exact equality.
	DurationToleranceNanos int64
	// MaxSamplesPerSegment bounds work per fingerprint.
	MaxSamplesPerSegment int
}

// DefaultConfig uses a 16-byte lookup key, which makes accidental collisions
// irrelevant in production while still exercisable by tests that lower it.
func DefaultConfig() Config {
	return Config{ShortBytes: 16, DurationToleranceNanos: 0, MaxSamplesPerSegment: 1 << 20}
}

func (c Config) validate() error {
	if c.ShortBytes < 1 || c.ShortBytes > 32 {
		return fmt.Errorf("fingerprint: ShortBytes %d outside 1..32", c.ShortBytes)
	}
	if c.DurationToleranceNanos < 0 {
		return errors.New("fingerprint: negative duration tolerance")
	}
	if c.MaxSamplesPerSegment <= 0 {
		return errors.New("fingerprint: sample limit must be positive")
	}
	return nil
}

// Sample is one coded sample handed to the fingerprint stage.
type Sample struct {
	// Digests are the sha256 of the sample payload bytes. Callers hash
	// incrementally so the fingerprint stage never retains payload memory.
	Digest   [32]byte
	Size     int
	Duration int64
	Keyframe bool
}

// Fingerprint is the stable identity of one segment.
type Fingerprint struct {
	// Content hashes the canonical sample sequence, not the container.
	Content [32]byte
	// Sampled hashes a fixed subset (first, middle, last), letting a cheap
	// pre-check reject collisions before the full comparison.
	Sampled [32]byte
	// Short is the lookup key derived from Content.
	Short uint64
	// DurationNanos is the sum of sample durations.
	DurationNanos int64
	SampleCount   int
	Bytes         int64
	// Keyframes is the number of independently decodable samples.
	Keyframes int
}

// Compute builds a fingerprint. Sample order is significant and preserved.
func (c Config) Compute(samples []Sample) (Fingerprint, error) {
	var fp Fingerprint
	if err := c.validate(); err != nil {
		return fp, err
	}
	if len(samples) == 0 {
		return fp, errors.New("fingerprint: no samples")
	}
	if len(samples) > c.MaxSamplesPerSegment {
		return fp, mediaerr.New(mediaerr.KindInvalidBox, -1, "", 0,
			fmt.Sprintf("segment has %d samples, limit is %d", len(samples), c.MaxSamplesPerSegment))
	}
	h := sha256.New()
	var hdr [16]byte
	for _, s := range samples {
		binary.BigEndian.PutUint64(hdr[0:], uint64(s.Duration))
		binary.BigEndian.PutUint32(hdr[8:], uint32(s.Size))
		if s.Keyframe {
			hdr[12] = 1
		} else {
			hdr[12] = 0
		}
		h.Write(hdr[:13])
		h.Write(s.Digest[:])
		fp.DurationNanos += s.Duration
		fp.Bytes += int64(s.Size)
		if s.Keyframe {
			fp.Keyframes++
		}
	}
	copy(fp.Content[:], h.Sum(nil))
	fp.SampleCount = len(samples)

	hs := sha256.New()
	for _, i := range sampleIndices(len(samples)) {
		hs.Write(samples[i].Digest[:])
	}
	copy(fp.Sampled[:], hs.Sum(nil))

	// The lookup key narrows Content to ShortBytes. Folding the excess bits in
	// (rather than masking) keeps every digest byte contributing to the key.
	fp.Short = binary.BigEndian.Uint64(fp.Content[:8])
	if c.ShortBytes < 8 {
		fp.Short ^= binary.BigEndian.Uint64(fp.Content[8:16])
		fp.Short ^= binary.BigEndian.Uint64(fp.Content[16:24])
		fp.Short ^= binary.BigEndian.Uint64(fp.Content[24:32])
		fp.Short &= ^uint64(0) >> (uint(8-c.ShortBytes) * 8)
	}
	return fp, nil
}

// sampleIndices picks first, middle and last without allocating for small
// inputs.
func sampleIndices(n int) []int {
	switch {
	case n == 1:
		return []int{0}
	case n == 2:
		return []int{0, 1}
	default:
		return []int{0, n / 2, n - 1}
	}
}

// Entry is one already-accepted segment in the dedup index.
type Entry struct {
	SegmentIndex int
	Fingerprint  Fingerprint
	// Source describes where the content came from, for the duplicate report.
	Source string
}

// Index maps lookup keys to accepted entries.
type Index struct {
	cfg     Config
	byShort map[uint64][]Entry
}

// NewIndex creates an empty dedup index.
func NewIndex(cfg Config) (*Index, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Index{cfg: cfg, byShort: map[uint64][]Entry{}}, nil
}

// Decision is the dedup verdict for one segment.
type Decision struct {
	Duplicate bool
	// SourceSegment is the already-accepted segment this one duplicates.
	SourceSegment int
	Source        string
	// Collision is set when the lookup key matched but verification proved
	// the content different. Content is always kept in that case.
	Collision bool
	// Reason is a stable, human-readable explanation.
	Reason string
}

// Admit evaluates fp against the index. A duplicate is reported only when
// every recheck passes; otherwise the entry is added and content is kept.
func (idx *Index) Admit(fp Fingerprint, source string) Decision {
	candidates := idx.byShort[fp.Short]
	for _, cand := range candidates {
		if cand.Fingerprint.SampleCount != fp.SampleCount {
			continue
		}
		if d := abs64(cand.Fingerprint.DurationNanos - fp.DurationNanos); d > idx.cfg.DurationToleranceNanos {
			continue
		}
		if cand.Fingerprint.Sampled != fp.Sampled {
			// Same duration and lookup key but different bytes: a genuine
			// near-collision. Keep both and say so.
			return Decision{
				Collision: true,
				Reason: fmt.Sprintf("short key matched segment %d but sampled digest differs; content kept",
					cand.SegmentIndex),
			}
		}
		if cand.Fingerprint.Content != fp.Content {
			return Decision{
				Collision: true,
				Reason: fmt.Sprintf("full digest differs from segment %d after sampled match; content kept",
					cand.SegmentIndex),
			}
		}
		return Decision{
			Duplicate:     true,
			SourceSegment: cand.SegmentIndex,
			Source:        cand.Source,
			Reason: fmt.Sprintf("identical content to segment %d from %s (%d samples, %d ns)",
				cand.SegmentIndex, cand.Source, fp.SampleCount, fp.DurationNanos),
		}
	}
	return Decision{}
}

// Accept records an entry under its lookup key. Callers must call Admit first
// and must not Accept a segment reported as a duplicate.
func (idx *Index) Accept(entry Entry) {
	idx.byShort[entry.Fingerprint.Short] = append(idx.byShort[entry.Fingerprint.Short], entry)
}

// Len returns how many entries were accepted.
func (idx *Index) Len() int {
	n := 0
	for _, es := range idx.byShort {
		n += len(es)
	}
	return n
}

// Sources returns the distinct source labels in sorted order, for reporting.
func (idx *Index) Sources() []string {
	seen := map[string]bool{}
	var out []string
	for _, es := range idx.byShort {
		for _, e := range es {
			if !seen[e.Source] {
				seen[e.Source] = true
				out = append(out, e.Source)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Report summarises a dedup run.
type Report struct {
	Evaluated  int
	Duplicates int
	Collisions int
	Accepted   int
	Decisions  []Decision
}

// Run evaluates fingerprints in order, accepting non-duplicates.
// sources[i] labels the content origin of fingerprints[i].
func (idx *Index) Run(fps []Fingerprint, sources []string) Report {
	var rep Report
	for i, fp := range fps {
		src := ""
		if i < len(sources) {
			src = sources[i]
		}
		rep.Evaluated++
		d := idx.Admit(fp, src)
		rep.Decisions = append(rep.Decisions, d)
		switch {
		case d.Duplicate:
			rep.Duplicates++
		default:
			if d.Collision {
				rep.Collisions++
			}
			idx.Accept(Entry{SegmentIndex: i, Fingerprint: fp, Source: src})
			rep.Accepted++
		}
	}
	return rep
}

// HashPayload is the canonical per-sample digest used by callers.
func HashPayload(p []byte) [32]byte { return sha256.Sum256(p) }

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
