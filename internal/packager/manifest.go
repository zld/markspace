// Package packager renders the re-packaged output manifests for the local MP4
// subset and computes deterministic content hashes. Every listed segment must
// trace back to a pipeline output event so the manifest can never describe a
// boundary the media does not have.
package packager

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"mediaqc/internal/model"
)

// Segment is one output manifest entry.
type Segment struct {
	Sequence uint64
	URI      string
	StartMs  int64
	DurationMs float64
	Recovered bool
}

// Manifest is the re-packaged, deterministic output for one material.
type Manifest struct {
	MaterialID   string
	TargetDurSec float64
	Segments     []Segment
	// Integrity summarizes gap/recovery state for the playlist.
	UnrecoveredGaps []model.Gap
}

// Builder turns pipeline output events into a Manifest.
type Builder struct {
	materialID string
}

// NewBuilder creates a builder for one material.
func NewBuilder(materialID string) *Builder { return &Builder{materialID: materialID} }

// FromEvents requires every output event to carry a valid timebase and emits
// segments sorted by sequence for stable rendering.
func (b *Builder) FromEvents(evs []model.Event, gaps []model.Gap) (Manifest, error) {
	ordered := append([]model.Event(nil), evs...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Sequence < ordered[j].Sequence })

	m := Manifest{MaterialID: b.materialID, TargetDurSec: 4.0,
		UnrecoveredGaps: append([]model.Gap(nil), gaps...)}
	for _, ev := range ordered {
		if !ev.Timebase.Valid() {
			return m, model.NewError(model.CodeZeroTimescale, model.StagePackage,
				b.materialID, ev.TrackID,
				fmt.Sprintf("output event seq %d has no valid timebase", ev.Sequence))
		}
		startMs := ev.Start.Convert(millisTB()).Ticks()
		durMs := ev.Duration.Convert(millisTB()).Ticks()
		m.Segments = append(m.Segments, Segment{
			Sequence:   ev.Sequence,
			URI:        fmt.Sprintf("%s-seg-%d.m4s", b.materialID, ev.Sequence),
			StartMs:    startMs,
			DurationMs: float64(durMs),
			Recovered:  ev.Recovered,
		})
	}
	return m, nil
}

// RenderHLS renders a deterministic media playlist.
func (m Manifest) RenderHLS() string {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	fmt.Fprintf(&sb, "#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:%d\n",
		int(m.TargetDurSec+0.5), firstSeq(m.Segments))
	for _, s := range m.Segments {
		if s.Recovered {
			sb.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		fmt.Fprintf(&sb, "#EXTINF:%.3f,\n%s\n", s.DurationMs/1000.0, s.URI)
	}
	if len(m.UnrecoveredGaps) > 0 {
		fmt.Fprintf(&sb, "#EXT-X-COMMENT:unrecovered_gaps=%d\n", len(m.UnrecoveredGaps))
	}
	sb.WriteString("#EXT-X-ENDLIST\n")
	return sb.String()
}

// SHA256 hashes the rendered playlist; identical inputs produce identical
// hashes across runs.
func (m Manifest) SHA256() string {
	sum := sha256.Sum256([]byte(m.RenderHLS()))
	return hex.EncodeToString(sum[:])
}

func firstSeq(segs []Segment) uint64 {
	if len(segs) == 0 {
		return 0
	}
	return segs[0].Sequence
}
