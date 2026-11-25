package segment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// Representation describes one renditions of the content (e.g. bitrate tier).
type Representation struct {
	ID            string `json:"id"`
	BitrateBps    int    `json:"bitrate_bps"`
	PacketTicks   int64  `json:"packet_ticks"`
	InitFile      string `json:"init_file"`
	SegmentPrefix string `json:"segment_prefix"`
}

// Manifest is the durable, content-addressed description of a full run.
// Field order is fixed so marshaling the same run twice yields byte-identical
// output and a stable manifest SHA-256.
type Manifest struct {
	FormatVersion   int              `json:"format_version"`
	GeneratedAt     string           `json:"generated_at"`
	ClockHz         int64            `json:"clock_hz"`
	SegmentTicks    int64            `json:"segment_ticks"`
	SegmentCount    int              `json:"segment_count"`
	Representations []Representation `json:"representations"`
	Segments        []*Descriptor    `json:"segments"`
	MaxAlignDrift   int64            `json:"max_alignment_delta_ticks"`
	// ManifestSHA256 is the digest of every other field, recomputed over a
	// canonical rendering with this field set to the empty string. It lets a
	// verifier confirm a manifest was not tampered with after writing.
	ManifestSHA256 string `json:"manifest_sha256"`
	// Corruption lists every reported defect with its input offset/PID.
	Corruption []CorruptionReport `json:"corruption,omitempty"`
}

// CorruptionReport is the manifest-facing view of a parser/clock defect.
type CorruptionReport struct {
	Kind   string `json:"kind"`
	Offset int64  `json:"offset"`
	PID    uint16 `json:"pid"`
	Detail string `json:"detail,omitempty"`
}

// NewManifest assembles a manifest and normalizes ordering so output is
// deterministic: representations by ID, segments by (representation, index).
// The timestamp is fixed to the Unix epoch rather than wall-clock time, so
// re-running the same input produces a byte-identical manifest and digest.
func NewManifest(
	baseTicks int64,
	reps []Representation,
	segs []*Descriptor,
	maxDrift int64,
	corruption []CorruptionReport,
) *Manifest {
	repsCopy := append([]Representation(nil), reps...)
	sort.Slice(repsCopy, func(i, j int) bool { return repsCopy[i].ID < repsCopy[j].ID })

	segsCopy := append([]*Descriptor(nil), segs...)
	sort.Slice(segsCopy, func(i, j int) bool {
		if segsCopy[i].Index != segsCopy[j].Index {
			return segsCopy[i].Index < segsCopy[j].Index
		}
		return segsCopy[i].Representation < segsCopy[j].Representation
	})

	corCopy := append([]CorruptionReport(nil), corruption...)
	sort.SliceStable(corCopy, func(i, j int) bool {
		if corCopy[i].Offset != corCopy[j].Offset {
			return corCopy[i].Offset < corCopy[j].Offset
		}
		return corCopy[i].Kind < corCopy[j].Kind
	})

	count := 0
	if len(segsCopy) > 0 {
		last := -1
		for _, d := range segsCopy {
			if d.Index != last {
				count++
				last = d.Index
			}
		}
	}
	return &Manifest{
		FormatVersion:   1,
		GeneratedAt:     time.Unix(0, 0).UTC().Format(time.RFC3339),
		ClockHz:         27_000_000,
		SegmentTicks:    baseTicks,
		SegmentCount:    count,
		Representations: repsCopy,
		Segments:        segsCopy,
		MaxAlignDrift:   maxDrift,
		Corruption:      corCopy,
	}
}

// Marshal renders the manifest with fixed field order (struct order) and no
// HTML escaping, which keeps the digest stable across Go versions.
func (m *Manifest) Marshal() ([]byte, error) {
	buf := jsonBufferPool.Get().(*jsonBuffer)
	defer jsonBufferPool.Put(buf)
	buf.reset()
	enc := json.NewEncoder(&buf.b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	out := make([]byte, buf.b.Len())
	copy(out, buf.b.Bytes())
	return out, nil
}

// SHA256 returns the hex digest of the canonical marshaled manifest. Because
// ManifestSHA256 is part of the document, it is first cleared, the digest is
// computed over the remaining fields, then restored, so the recorded value
// describes the rest of the manifest rather than itself.
func (m *Manifest) SHA256() (string, error) {
	saved := m.ManifestSHA256
	m.ManifestSHA256 = ""
	raw, err := m.Marshal()
	m.ManifestSHA256 = saved
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Seal computes the self-describing digest and stores it on the manifest.
func (m *Manifest) Seal() error {
	digest, err := m.SHA256()
	if err != nil {
		return err
	}
	m.ManifestSHA256 = digest
	return nil
}
