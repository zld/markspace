package media

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"

	"github.com/archive/mediakernel/internal/manifest"
	"github.com/archive/mediakernel/internal/timeline"
)

// FingerprintConfig controls content hashing. Tolerance 0 requires bit-exact
// PCM; higher tolerance quantizes each 16-bit sample into progressively coarser
// amplitude bins so minor level differences still match, while structural
// differences (different sample count or large amplitude changes) do not.
type FingerprintConfig struct {
	Tolerance int // 0..3; quantization step is 2^(2*Tolerance)
}

// MaxTolerance bounds the coarsest supported quantization.
const MaxTolerance = 3

// Validate the tolerance range.
func (c FingerprintConfig) Validate() error {
	if c.Tolerance < 0 || c.Tolerance > MaxTolerance {
		return timeline.NewError(timeline.KindInvalidInput, "fingerprint tolerance %d outside 0..%d", c.Tolerance, MaxTolerance)
	}
	return nil
}

func (c FingerprintConfig) quantStep() int16 {
	return int16(1) << (2 * uint(c.Tolerance)) // 1, 4, 16, 64
}

// Fingerprint computes the content hash for one segment's PCM. The sample count
// is mixed into the hash so content of different length never collides, and
// quantization makes the comparison tolerant of small amplitude variation.
func Fingerprint(pcm []byte, cfg FingerprintConfig) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	n := len(pcm) / 2
	h := sha256.New()
	var lenBuf [8]byte
	binary.LittleEndian.PutUint64(lenBuf[:], uint64(n))
	h.Write(lenBuf[:])
	step := cfg.quantStep()
	if step <= 1 {
		h.Write(pcm[:n*2])
	} else {
		quantized := make([]byte, n*2)
		for i := 0; i < n; i++ {
			v := int16(binary.LittleEndian.Uint16(pcm[2*i:]))
			q := (v / step) * step // truncate toward zero into amplitude bins
			binary.LittleEndian.PutUint16(quantized[2*i:], uint16(q))
		}
		h.Write(quantized)
	}
	return "fp:" + hex.EncodeToString(h.Sum(nil)), nil
}

// SegmentContent bundles a segment's timeline range with its audio bytes.
type SegmentContent struct {
	ID      string
	StartUS int64
	EndUS   int64
	PCM     []byte
}

// DedupResult is one fingerprint record after duplicate analysis.
type DedupResult struct {
	Record manifest.FingerprintRecord
}

// FingerprintAndDedup hashes every segment and groups equal fingerprints. A
// duplicate never loses its source mapping: each record carries every timeline
// range producing that hash. Segments separated by a confirmed gap are never
// merged even when their bytes hash equally, since joining them would fabricate
// continuity over unknown content.
func FingerprintAndDedup(segments []SegmentContent, gaps []manifest.LossGap, cfg FingerprintConfig) ([]manifest.FingerprintRecord, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	type group struct {
		hash   string
		ranges []manifest.Range
		dup    bool
	}
	groups := make(map[string]*group)
	var order []string
	for _, seg := range segments {
		if seg.EndUS <= seg.StartUS {
			return nil, timeline.NewError(timeline.KindInvalidInput, "segment %q has invalid range", seg.ID)
		}
		hash, err := Fingerprint(seg.PCM, cfg)
		if err != nil {
			return nil, err
		}
		g, ok := groups[hash]
		if !ok {
			g = &group{hash: hash}
			groups[hash] = g
			order = append(order, hash)
		}
		g.ranges = append(g.ranges, manifest.Range{StartUS: seg.StartUS, EndUS: seg.EndUS})
	}

	records := make([]manifest.FingerprintRecord, 0, len(order))
	for _, hash := range order {
		g := groups[hash]
		ranges := g.ranges
		sort.Slice(ranges, func(i, j int) bool { return ranges[i].StartUS < ranges[j].StartUS })
		dup := len(ranges) > 1
		// A same-hash pair whose ranges straddle a gap must not be treated as a
		// mergeable duplicate: keep both ranges but clear the duplicate flag.
		if dup && spansAnyGap(ranges, gaps) {
			dup = false
		}
		records = append(records, manifest.FingerprintRecord{
			Hash:         hash,
			StartUS:      ranges[0].StartUS,
			EndUS:        ranges[len(ranges)-1].EndUS,
			Duplicate:    dup,
			SourceRanges: ranges,
		})
	}
	// Stable output ordering by start time.
	sort.Slice(records, func(i, j int) bool { return records[i].StartUS < records[j].StartUS })
	return records, nil
}

// spansAnyGap reports whether the union of ranges has any gap strictly between
// two of its members, i.e. content that would have to be invented to merge them.
func spansAnyGap(ranges []manifest.Range, gaps []manifest.LossGap) bool {
	for i := 0; i < len(ranges); i++ {
		for j := i + 1; j < len(ranges); j++ {
			lo := ranges[i].EndUS
			hi := ranges[j].StartUS
			for _, g := range gaps {
				if g.StartUS >= lo && g.EndUS <= hi {
					return true
				}
			}
		}
	}
	return false
}
