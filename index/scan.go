// Package index turns a probed container into a versioned, digest-addressed
// index and persists it for reuse.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/zld/mediapipe/media"
)

// Version identifies the on-disk index format. Bumping it invalidates any
// cached index without needing to parse the payload.
const Version = "idx-v1"

// Keyframe is a keyframe position in both track ticks and media time.
type Keyframe struct {
	Sample int    `json:"sample"`
	Ticks  int64  `json:"ticks"`   // exact track tick offset from the first sample
	TimeUS int64  `json:"time_us"` // media time of the sample start
}

// TrackIndex is the persisted per-track timing and keyframe index.
type TrackIndex struct {
	ID          uint32     `json:"id"`
	Kind        string     `json:"kind"`
	Handler     string     `json:"handler"`
	Timescale   uint32     `json:"timescale"`
	StartUS     int64      `json:"start_us"`
	EndUS       int64      `json:"end_us"`
	DurationUS  int64      `json:"duration_us"`
	FrameTicks  uint32     `json:"frame_ticks"`
	SampleCount int        `json:"sample_count"`
	Keyframes   []Keyframe `json:"keyframes"`
	Samples     []media.Sample `json:"samples,omitempty"`
}

// Index is the complete, reproducible description of one container input.
type Index struct {
	Version       string          `json:"version"`
	InputSHA256   string          `json:"input_sha256"`
	InputSize     int64           `json:"input_size"`
	Brand         string          `json:"brand"`
	MovieTimescale uint32         `json:"movie_timescale"`
	MovieDurationUS int64         `json:"movie_duration_us"`
	Tracks        []TrackIndex    `json:"tracks"`
	Issues        []media.Issue   `json:"issues"`
	Digest        string          `json:"digest"`
}

// Track returns the indexed track with the given kind, or nil.
func (idx *Index) Track(kind string) *TrackIndex {
	for i := range idx.Tracks {
		if idx.Tracks[i].Kind == kind {
			return &idx.Tracks[i]
		}
	}
	return nil
}

// DigestInputs is the canonical digest of the inputs that determine index
// content: the file hash and the index format version.
func DigestInputs(inputSHA string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", Version, inputSHA)
	return hex.EncodeToString(h.Sum(nil))
}

// Build creates an index from a probe result. Building the same container with
// the same limits always yields the same Digest.
func Build(c *media.Container) (*Index, error) {
	idx := &Index{
		Version:         Version,
		InputSHA256:     c.SHA256,
		InputSize:       c.Size,
		Brand:           c.Brand,
		MovieTimescale:  c.MovieTimescale,
		MovieDurationUS: c.MovieDurationUS,
		Issues:          append([]media.Issue{}, c.Issues...),
	}
	for _, tr := range c.Tracks {
		ti := TrackIndex{
			ID: tr.ID, Kind: tr.Kind, Handler: tr.Handler,
			Timescale: tr.Timescale, StartUS: tr.StartUS, EndUS: tr.EndUS,
			DurationUS: tr.MediaUS, FrameTicks: tr.FrameTicks,
			SampleCount: tr.SampleCount,
		}
		// Keyframes are recorded in exact tick space as well as media time so
		// alignment does not need to re-derive them from float math.
		scale := int64(1)
		if tr.Timescale > 0 && tr.Samples != nil {
			for _, kf := range tr.Keyframes {
				if kf < 1 || kf > len(tr.Samples) {
					continue
				}
				s := tr.Samples[kf-1]
				ti.Keyframes = append(ti.Keyframes, Keyframe{
					Sample: kf,
					Ticks:  int64(s.StartUS) * int64(tr.Timescale) / scale / 1_000_000,
					TimeUS: s.StartUS,
				})
			}
			ti.Samples = tr.Samples
		}
		sort.SliceStable(ti.Keyframes, func(a, b int) bool {
			return ti.Keyframes[a].Ticks < ti.Keyframes[b].Ticks
		})
		idx.Tracks = append(idx.Tracks, ti)
	}
	d, err := digestIndex(idx)
	if err != nil {
		return nil, err
	}
	idx.Digest = d
	return idx, nil
}

// digestIndex computes the digest over canonical JSON of the index body with
// Digest cleared. json.Marshal sorts map keys, slices are emitted in order, and
// no floating point fields exist, so this is deterministic.
func digestIndex(idx *Index) (string, error) {
	clone := *idx
	clone.Digest = ""
	b, err := json.Marshal(&clone)
	if err != nil {
		return "", media.WrapError(media.KindInternal, "", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Validate checks internal consistency and the stored digest. A failed
// validation means the cached index must be discarded and rebuilt.
func (idx *Index) Validate() error {
	if idx.Version != Version {
		return media.NewError(media.KindCheckpoint, "", "index version %q is not %q", idx.Version, Version)
	}
	want, err := digestIndex(idx)
	if err != nil {
		return err
	}
	if idx.Digest != want {
		return media.NewError(media.KindCheckpoint, "", "index digest mismatch")
	}
	for _, t := range idx.Tracks {
		for _, k := range t.Keyframes {
			if k.Sample < 1 || k.Sample > t.SampleCount {
				return media.NewError(media.KindCorruptBox, "", "keyframe sample %d out of range", k.Sample)
			}
		}
	}
	return nil
}

// Canonical returns the canonical JSON encoding used for digests and storage.
func (idx *Index) Canonical() ([]byte, error) {
	clone := *idx
	b, err := json.MarshalIndent(&clone, "", "  ")
	if err != nil {
		return nil, media.WrapError(media.KindInternal, "", err)
	}
	return b, nil
}
