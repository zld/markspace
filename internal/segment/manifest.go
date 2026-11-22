package segment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"mediapipeline/internal/container"
	"mediapipeline/internal/media"
)

// ManifestSchema is the on-disk manifest format version.
const ManifestSchema = 1

// SegmentEntry describes one produced segment.
type SegmentEntry struct {
	Index      int     `json:"index"`
	StartMS    float64 `json:"start_ms"`
	EndMS      float64 `json:"end_ms"`
	DurationMS float64 `json:"duration_ms"`
	StartNanos int64   `json:"start_nanos"`
	EndNanos   int64   `json:"end_nanos"`
	ByteSize   int64   `json:"byte_size"`
	SHA256     string  `json:"sha256"`
	VideoStart int     `json:"video_sample_start"`
	AudioStart int     `json:"audio_sample_start"`
	AudioCount int     `json:"audio_sample_count"`
}

// Representation is one manifest entry per bitrate.
type Representation struct {
	Name       string         `json:"name"`
	Bitrate    int            `json:"bitrate_kbps"`
	TrackID    uint32         `json:"track_id"`
	Timescale  uint32         `json:"timescale"`
	Segments   []SegmentEntry `json:"segments"`
	TotalBytes int64          `json:"total_bytes"`
}

// Manifest is the complete segment list for a piece of content.
type Manifest struct {
	Schema            int              `json:"schema"`
	ContentID         string           `json:"content_id"`
	DurationMS        float64          `json:"duration_ms"`
	TargetMS          float64          `json:"target_ms"`
	AlignedBoundaries int              `json:"aligned_boundaries"`
	Representations   []Representation `json:"representations"`
	// SegmentCount is the shared number of segments (identical for all
	// representations when alignment holds).
	SegmentCount int    `json:"segment_count"`
	SHA256       string `json:"sha256,omitempty"` // filled by Seal
}

// BuildInput ties a representation to its parsed track and payload reader.
type BuildInput struct {
	Name    string
	Bitrate int
	Track   *container.Track
	// Read returns the raw sample bytes for a sample index.
	Read func(index int) ([]byte, error)
	// Audio, when non-nil, is sliced on the same boundaries.
	Audio *container.Track
	// ReadAudio returns raw audio sample bytes.
	ReadAudio func(index int) ([]byte, error)
}

// Build assembles a manifest from an aligned plan. Segment byte size and hash
// are computed from the actual sample payloads, so two runs over the same
// input produce the same hashes.
func Build(plan *Plan, inputs []BuildInput, target media.Nano) (*Manifest, error) {
	if len(inputs) == 0 {
		return nil, media.NewError(media.InvalidInput, "manifest needs at least one representation")
	}
	if len(inputs) != len(plan.PerTarget) {
		return nil, media.NewError(media.InvariantViolation,
			"%d inputs but %d target plans", len(inputs), len(plan.PerTarget))
	}
	m := &Manifest{
		Schema:            ManifestSchema,
		DurationMS:        media.Millis(plan.Duration),
		TargetMS:          media.Millis(target),
		AlignedBoundaries: len(plan.Boundaries),
		SegmentCount:      len(plan.Boundaries),
	}
	for i, in := range inputs {
		if in.Track == nil {
			return nil, media.NewError(media.InvalidInput, "representation %q has no track", in.Name)
		}
		if in.Read == nil {
			return nil, media.NewError(media.InvalidInput, "representation %q has no reader", in.Name)
		}
		tp := plan.PerTarget[i]
		if len(tp.VideoStarts) != len(plan.Boundaries) {
			return nil, media.NewError(media.InvariantViolation,
				"representation %q has %d starts but %d boundaries", in.Name, len(tp.VideoStarts), len(plan.Boundaries))
		}
		rep := Representation{
			Name:      in.Name,
			Bitrate:   in.Bitrate,
			TrackID:   in.Track.ID,
			Timescale: in.Track.Timescale,
		}
		for si, b := range plan.Boundaries {
			end := plan.Duration
			if si+1 < len(plan.Boundaries) {
				end = plan.Boundaries[si+1].Start
			}
			if end <= b.Start {
				return nil, media.NewError(media.InvariantViolation,
					"segment %d has non-positive duration", si)
			}
			first := tp.VideoStarts[si]
			last := len(in.Track.Samples)
			if si+1 < len(tp.VideoStarts) {
				last = tp.VideoStarts[si+1]
			}
			if first >= last {
				return nil, media.NewError(media.InvariantViolation,
					"segment %d covers no samples", si)
			}
			h := sha256.New()
			var size int64
			for idx := first; idx < last; idx++ {
				payload, err := in.Read(idx)
				if err != nil {
					return nil, media.WrapError(media.InvalidInput, err,
						"reading video sample %d of %q", idx, in.Name)
				}
				h.Write(payload)
				size += int64(len(payload))
			}
			audioStart, audioCount := 0, 0
			if in.Audio != nil {
				if len(tp.AudioStarts) != len(plan.Boundaries) {
					return nil, media.NewError(media.InvariantViolation,
						"representation %q audio starts do not match boundaries", in.Name)
				}
				if in.ReadAudio == nil {
					return nil, media.NewError(media.InvalidInput,
						"representation %q has audio but no audio reader", in.Name)
				}
				audioStart = tp.AudioStarts[si]
				audioEnd := len(in.Audio.Samples)
				if si+1 < len(tp.AudioStarts) {
					audioEnd = tp.AudioStarts[si+1]
				}
				for idx := audioStart; idx < audioEnd; idx++ {
					payload, err := in.ReadAudio(idx)
					if err != nil {
						return nil, media.WrapError(media.InvalidInput, err,
							"reading audio sample %d of %q", idx, in.Name)
					}
					h.Write(payload)
					size += int64(len(payload))
				}
				audioCount = audioEnd - audioStart
			}
			rep.Segments = append(rep.Segments, SegmentEntry{
				Index:      si,
				StartMS:    media.Millis(b.Start),
				EndMS:      media.Millis(end),
				DurationMS: media.Millis(end - b.Start),
				StartNanos: int64(b.Start),
				EndNanos:   int64(end),
				ByteSize:   size,
				SHA256:     hex.EncodeToString(h.Sum(nil)),
				VideoStart: first,
				AudioStart: audioStart,
				AudioCount: audioCount,
			})
			rep.TotalBytes += size
		}
		m.Representations = append(m.Representations, rep)
	}
	m.ContentID = contentID(m)
	return m, nil
}

// contentID derives a stable identifier from the aligned boundary list and
// representation names, independent of map iteration order.
func contentID(m *Manifest) string {
	h := sha256.New()
	for _, b := range m.Representations {
		h.Write([]byte(b.Name))
		h.Write([]byte{0})
	}
	var bounds []int64
	if len(m.Representations) > 0 {
		for _, s := range m.Representations[0].Segments {
			bounds = append(bounds, s.StartNanos)
		}
	}
	sort.Slice(bounds, func(i, j int) bool { return bounds[i] < bounds[j] })
	for _, b := range bounds {
		var buf [8]byte
		for i := 0; i < 8; i++ {
			buf[i] = byte(uint64(b) >> (8 * (7 - i)))
		}
		h.Write(buf[:])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Seal computes the manifest digest over its canonical JSON form. Callers
// persist the returned JSON and this digest together.
func (m *Manifest) Seal() ([]byte, string, error) {
	clone := *m
	clone.SHA256 = ""
	data, err := json.Marshal(&clone)
	if err != nil {
		return nil, "", media.WrapError(media.InvalidInput, err, "marshalling manifest")
	}
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	return data, hexSum, nil
}

// Validate checks structural invariants that must hold for any manifest the
// pipeline considers valid.
func (m *Manifest) Validate() error {
	if m.Schema != ManifestSchema {
		return media.NewError(media.InvariantViolation,
			"manifest schema %d unsupported", m.Schema)
	}
	if len(m.Representations) == 0 {
		return media.NewError(media.InvariantViolation, "manifest has no representations")
	}
	want := len(m.Representations[0].Segments)
	if m.SegmentCount != want {
		return media.NewError(media.InvariantViolation,
			"segment_count %d but first representation has %d", m.SegmentCount, want)
	}
	if m.AlignedBoundaries != want {
		return media.NewError(media.InvariantViolation,
			"aligned_boundaries %d but %d segments", m.AlignedBoundaries, want)
	}
	seen := make(map[string]bool)
	for _, rep := range m.Representations {
		if seen[rep.Name] {
			return media.NewError(media.InvariantViolation, "duplicate representation %q", rep.Name)
		}
		seen[rep.Name] = true
		if len(rep.Segments) != want {
			return media.NewError(media.InvariantViolation,
				"representation %q has %d segments, want %d", rep.Name, len(rep.Segments), want)
		}
		var total int64
		for i, s := range rep.Segments {
			if s.Index != i {
				return media.NewError(media.InvariantViolation,
					"representation %q segment %d has index %d", rep.Name, i, s.Index)
			}
			if s.EndNanos <= s.StartNanos {
				return media.NewError(media.InvariantViolation,
					"representation %q segment %d is empty", rep.Name, i)
			}
			if s.SHA256 == "" {
				return media.NewError(media.InvariantViolation,
					"representation %q segment %d has no hash", rep.Name, i)
			}
			if i > 0 && s.StartNanos != rep.Segments[i-1].EndNanos {
				return media.NewError(media.InvariantViolation,
					"representation %q segment %d is not contiguous", rep.Name, i)
			}
			total += s.ByteSize
		}
		if total != rep.TotalBytes {
			return media.NewError(media.InvariantViolation,
				"representation %q total_bytes %d but segments sum to %d", rep.Name, rep.TotalBytes, total)
		}
	}
	// Every representation must share the first representation's boundaries.
	base := m.Representations[0].Segments
	for _, rep := range m.Representations[1:] {
		for i, s := range rep.Segments {
			if s.StartNanos != base[i].StartNanos || s.EndNanos != base[i].EndNanos {
				return media.NewError(media.BoundaryNotFound,
					"representation %q segment %d boundary [%d,%d) differs from %q [%d,%d)",
					rep.Name, i, s.StartNanos, s.EndNanos,
					m.Representations[0].Name, base[i].StartNanos, base[i].EndNanos)
			}
		}
	}
	return nil
}

// Hash returns the sealed manifest digest, computing it if needed.
func (m *Manifest) Hash() (string, error) {
	_, sum, err := m.Seal()
	return sum, err
}
