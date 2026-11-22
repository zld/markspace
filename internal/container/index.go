package container

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"archivist/internal/model"
	"github.com/pion/rtp"
)

// BoxDigest computes a canonical digest over the box tree: only structural
// facts (type nesting, sizes, table geometry) participate, so two files with
// the same normalized layout hash identically even if media bytes differ.
func BoxDigest(root *ParseResult) string {
	h := sha256.New()
	var walk func(boxes []*Box, depth int)
	walk = func(boxes []*Box, depth int) {
		for _, b := range boxes {
			fmt.Fprintf(h, "%d:%s:%d\n", depth, b.Type, b.Size())
			if len(b.Children) > 0 {
				walk(b.Children, depth+1)
			} else {
				fmt.Fprintf(h, "payload:%x\n", leafFingerprint(b))
			}
		}
	}
	walk(root.Boxes, 0)
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)
}

// leafFingerprint hashes only the length-prefixed payload shape. Raw media
// (mdat) is excluded so the digest reflects structure, not content bytes.
func leafFingerprint(b *Box) []byte {
	if b.Type == BoxMdat {
		return []byte(fmt.Sprintf("mdat-len:%d", b.PayloadSize()))
	}
	h := sha256.Sum256(b.Payload)
	return h[:8]
}

// TrackIndex is the rebuilt random-access index for one track.
type TrackIndex struct {
	TrackID     uint32
	Kind        string
	Timescale   uint32
	SampleCount int
	Keyframes   []uint32 // 0-based sample numbers
	// BoundaryDTS are the media times of keyframes in track units.
	BoundaryDTS []uint64
	FirstDTS    uint64
	LastEndDTS  uint64
	IndexMode   string
}

// BuildIndex constructs random-access indexes from normalized tracks.
func BuildIndex(pf *ParsedFile) ([]*TrackIndex, error) {
	out := make([]*TrackIndex, 0, len(pf.Tracks))
	for _, tr := range pf.Tracks {
		if len(tr.Samples) == 0 {
			return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, tr.ID,
				"track has no samples")
		}
		ti := &TrackIndex{
			TrackID:     tr.ID,
			Kind:        tr.Kind,
			Timescale:   tr.Timescale,
			SampleCount: len(tr.Samples),
			IndexMode:   tr.IndexMode,
			FirstDTS:    tr.Samples[0].DTS,
		}
		for i, s := range tr.Samples {
			if s.IsKeyframe {
				ti.Keyframes = append(ti.Keyframes, uint32(i))
				ti.BoundaryDTS = append(ti.BoundaryDTS, s.DTS)
			}
		}
		last := tr.Samples[len(tr.Samples)-1]
		ti.LastEndDTS = last.DTS + uint64(last.Duration)
		if tr.Kind == model.KindVideo && len(ti.Keyframes) == 0 {
			return nil, model.AtTrack(model.StageIndex, model.ErrMissingKeyframeIdx, tr.ID,
				"video track without any keyframe")
		}
		out = append(out, ti)
	}
	return out, nil
}

// SequenceCheck validates a synthetic RTP sequence number stream derived from
// sample order. It uses pion/rtp's fixed sequencer so wrap-around behavior is
// checked against the same implementation used in real transport code.
func SequenceCheck(count int, start uint16) error {
	seq := rtp.NewFixedSequencer(start)
	prev := start
	for i := 0; i < count; i++ {
		got := seq.NextSequenceNumber()
		expected := prev + 1
		if got != expected {
			return fmt.Errorf("sequence discontinuity at %d: got %d want %d", i, got, expected)
		}
		prev = got
	}
	return nil
}

// Summary builds the deterministic model.Summary of a parsed container.
func Summary(name string, pf *ParsedFile) model.Summary {
	s := model.Summary{
		AssetName:      name,
		TrackCount:     len(pf.Tracks),
		MovieTimescale: pf.MovieTimescale,
		MovieDuration:  pf.MovieDuration,
		BoxDigest:      BoxDigest(pf.Root),
	}
	for _, tr := range pf.Tracks {
		s.SampleCount += len(tr.Samples)
		for _, smp := range tr.Samples {
			if smp.IsKeyframe {
				s.KeyframeCount++
			}
		}
	}
	return s
}

// CanonicalBoxReport renders a stable, diffable textual box tree for tests.
// File order is preserved — it is semantically meaningful in ISO BMFF.
func CanonicalBoxReport(root *ParseResult) string {
	var sb strings.Builder
	var walk func(boxes []*Box, depth int)
	walk = func(boxes []*Box, depth int) {
		for _, b := range boxes {
			fmt.Fprintf(&sb, "%s%s size=%d\n", strings.Repeat("  ", depth), b.Type, b.Size())
			if len(b.Children) > 0 {
				walk(b.Children, depth+1)
			}
		}
	}
	walk(root.Boxes, 0)
	return sb.String()
}
