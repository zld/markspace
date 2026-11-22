package pipeline

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"mediacore/index"
	"mediacore/media"
)

// NormalizeMode selects how frame pixels are canonicalized before hashing.
type NormalizeMode string

const (
	// NormalizeExact hashes the raw payload. Identical content with different
	// timestamps or metadata groups together; nothing else does.
	NormalizeExact NormalizeMode = "exact"
	// NormalizeBlock averages pixels onto a fixed grid and quantizes them so
	// capture noise below the quantum is ignored.
	NormalizeBlock NormalizeMode = "block"
)

// FingerprintConfig controls content normalization.
type FingerprintConfig struct {
	Mode NormalizeMode
	// Grid is the block-normalized thumbnail edge (e.g. 16 for 16x16).
	Grid int
	// Quantum is the bit depth kept after block averaging (1..8).
	Quantum int
}

// DefaultFingerprintConfig returns exact normalization.
func DefaultFingerprintConfig() FingerprintConfig {
	return FingerprintConfig{Mode: NormalizeExact, Grid: 16, Quantum: 8}
}

// Validate checks the fingerprint policy.
func (c FingerprintConfig) Validate() error {
	switch c.Mode {
	case NormalizeExact:
		return nil
	case NormalizeBlock:
		if c.Grid <= 0 || c.Grid > 256 {
			return media.New(media.CatInvalidParams, media.StageFingerprint, "block grid must be in 1..256")
		}
		if c.Quantum < 1 || c.Quantum > 8 {
			return media.New(media.CatInvalidParams, media.StageFingerprint, "quantum must be in 1..8")
		}
		return nil
	default:
		return media.New(media.CatInvalidParams, media.StageFingerprint, "unknown normalize mode %q", c.Mode)
	}
}

// Canonical returns the normalized content bytes for a frame. Byte equality on
// this slice is the definitive check when two fingerprints collide.
func (c FingerprintConfig) Canonical(f *media.Frame) []byte {
	if c.Quantum == 0 {
		c.Quantum = 8
	}
	switch c.Mode {
	case NormalizeBlock:
		return blockNormalize(f, c.Grid, c.Quantum)
	default:
		out := make([]byte, len(f.Payload))
		copy(out, f.Payload)
		return out
	}
}

// Fingerprint binds a digest to the canonical bytes it summarizes, so a hash
// collision can always be disproved by comparing the bytes.
type Fingerprint struct {
	Hex       string
	Canonical []byte
}

// Compute fingerprints a decoded frame. Dimensions are part of the digest
// input so a thumbnail of a different aspect cannot alias.
func (c FingerprintConfig) Compute(f *media.Frame) Fingerprint {
	canon := c.Canonical(f)
	h := sha256.New()
	var dims [4]byte
	binary.BigEndian.PutUint16(dims[0:2], f.Width)
	binary.BigEndian.PutUint16(dims[2:4], f.Height)
	h.Write(dims[:])
	h.Write(canon)
	return Fingerprint{Hex: hex.EncodeToString(h.Sum(nil)), Canonical: canon}
}

func blockNormalize(f *media.Frame, grid, quantum int) []byte {
	w, h := int(f.Width), int(f.Height)
	if w <= 0 || h <= 0 || len(f.Payload) < w*h {
		out := make([]byte, len(f.Payload))
		copy(out, f.Payload)
		return out
	}
	if grid > w {
		grid = w
	}
	if grid > h {
		grid = h
	}
	out := make([]byte, grid*grid)
	mask := byte(0xFF << (8 - quantum))
	for gy := 0; gy < grid; gy++ {
		y0 := gy * h / grid
		y1 := (gy + 1) * h / grid
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for gx := 0; gx < grid; gx++ {
			x0 := gx * w / grid
			x1 := (gx + 1) * w / grid
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sum, n int
			for y := y0; y < y1; y++ {
				row := f.Payload[y*w : y*w+w]
				for x := x0; x < x1; x++ {
					sum += int(row[x])
					n++
				}
			}
			avg := byte(0)
			if n > 0 {
				avg = byte(sum / n)
			}
			out[gy*grid+gx] = avg & mask
		}
	}
	return out
}

// DuplicateRef identifies one frame that shares content with a group leader.
type DuplicateRef struct {
	Seq    uint32 `json:"seq"`
	Ticks  int64  `json:"ticks"`
	Offset int64  `json:"offset"`
	Role   string `json:"role"`
}

// DuplicateGroup lists frames whose normalized content matches. Duplicates are
// references, not deletions; every frame stays in every other statistic.
type DuplicateGroup struct {
	Fingerprint string         `json:"fingerprint"`
	FirstSeq    uint32         `json:"first_seq"`
	FirstTicks  int64          `json:"first_ticks"`
	FirstMS     int64          `json:"first_ms"`
	Count       int            `json:"count"`
	Frames      []DuplicateRef `json:"frames"`
	Collisions  int            `json:"collisions,omitempty"`
}

// Deduper assigns content fingerprints and groups equal content. A fingerprint
// digest match is only accepted when the canonical bytes also match; otherwise
// the frame is treated as a distinct content and the collision is counted.
type Deduper struct {
	cfg        FingerprintConfig
	groups     []DuplicateGroup
	byDigest   map[string][]int // digest -> candidate group indexes
	byContent  map[string]int   // digest+canonical hash -> group index
	collisions int
}

// NewDeduper validates cfg and returns an empty deduper.
func NewDeduper(cfg FingerprintConfig) (*Deduper, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Deduper{
		cfg:       cfg,
		byDigest:  make(map[string][]int),
		byContent: make(map[string]int),
	}, nil
}

// msOf converts entry ticks to milliseconds at the default video clock. Callers
// with a non-default timescale should format with the shared clock instead.
func msOf(e index.Entry) int64 {
	return media.MustClock(media.DefaultTimescale).ToMS(e.Ticks)
}

// Add fingerprints one verified frame and returns its group index and whether
// the frame was a duplicate of an earlier frame.
func (d *Deduper) Add(e index.Entry, f *media.Frame) (int, bool, error) {
	if f.Width != e.Width || f.Height != e.Height {
		return 0, false, media.New(media.CatIndexInput, media.StageFingerprint,
			"frame dimensions %dx%d disagree with index %dx%d", f.Width, f.Height, e.Width, e.Height).
			WithOffset(e.Offset).WithSeq(e.Seq)
	}
	fp := d.cfg.Compute(f)
	contentKey := contentKeyOf(fp.Hex, fp.Canonical)
	if gi, ok := d.byContent[contentKey]; ok {
		d.groups[gi].Frames = append(d.groups[gi].Frames, DuplicateRef{
			Seq: e.Seq, Ticks: e.Ticks, Offset: e.Offset, Role: "duplicate",
		})
		d.groups[gi].Count++
		return gi, true, nil
	}
	// A shared digest with different canonical bytes is a real collision: keep
	// the new frame as its own group instead of dropping it.
	if cands := d.byDigest[fp.Hex]; len(cands) > 0 {
		d.collisions++
		for _, gi := range cands {
			d.groups[gi].Collisions++
		}
	}
	gi := len(d.groups)
	d.groups = append(d.groups, DuplicateGroup{
		Fingerprint: fp.Hex,
		FirstSeq:    e.Seq,
		FirstTicks:  e.Ticks,
		FirstMS:     msOf(e),
		Count:       1,
		Frames:      []DuplicateRef{{Seq: e.Seq, Ticks: e.Ticks, Offset: e.Offset, Role: "canonical"}},
	})
	d.byDigest[fp.Hex] = append(d.byDigest[fp.Hex], gi)
	d.byContent[contentKey] = gi
	return gi, false, nil
}

func contentKeyOf(digest string, canonical []byte) string {
	h := sha256.New()
	h.Write([]byte(digest))
	h.Write([]byte{0})
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil))
}

// Groups returns all content groups in first-seen order.
func (d *Deduper) Groups() []DuplicateGroup { return d.groups }

// DuplicateGroups returns only groups with more than one frame.
func (d *Deduper) DuplicateGroups() []DuplicateGroup {
	out := make([]DuplicateGroup, 0)
	for _, g := range d.groups {
		if g.Count > 1 {
			out = append(out, g)
		}
	}
	return out
}

// Collisions returns the number of digest collisions disproved by content.
func (d *Deduper) Collisions() int { return d.collisions }

// DuplicateFrameCount is the number of frames that duplicated earlier content.
func (d *Deduper) DuplicateFrameCount() int {
	n := 0
	for _, g := range d.groups {
		n += g.Count - 1
	}
	return n
}

// Describe renders a duplicate group for terminal output.
func (g DuplicateGroup) Describe() string {
	return fmt.Sprintf("fingerprint=%s frames=%d first_seq=%d first_ms=%d",
		short(g.Fingerprint), g.Count, g.FirstSeq, g.FirstMS)
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
