// Package sample generates deterministic archive fixtures: per-layer
// elementary streams, keyframe indexes, and the five required fault cases.
// Nothing is loaded from disk or the network.
package sample

import (
	"errors"
	"fmt"
	"math"

	"archivecheck/internal/codec"
	"archivecheck/internal/index"
)

// Case names accepted by the demo.
const (
	CaseOK              = "ok"
	CaseSplitStartCode  = "split-startcode"
	CaseCorruptIndex    = "corrupt-index"
	CaseOBUTruncated    = "obu-truncated"
	CaseKeyframeRegress = "keyframe-regress"
	CaseBoundarySkew    = "boundary-skew"
)

// NominalKeyframeIntervalMs is the generated keyframe cadence.
const NominalKeyframeIntervalMs = 250

// Layer is one generated representation.
type Layer struct {
	Name       string
	Codec      codec.Codec
	TargetKbps float64
	Timescale  uint32
	DurationMs int64
	Stream     []byte
	Index      index.Index

	frameOffsets []int64
	frameLengths []int64
}

// Archive is a complete generated fixture.
type Archive struct {
	Case       string
	Seed       int64
	ChunkBytes int
	DurationMs int64
	Layers     []Layer

	// frameLayout records generated frame extents per layer for fixtures that
	// need a precise damage location.
	frameLayout map[string][]frameExtent
}

type frameExtent struct {
	offset int64
	length int64
}

// LayerSpec describes one representation to build.
type LayerSpec struct {
	Name       string
	Codec      codec.Codec
	TargetKbps float64
	Timescale  uint32
}

// Cases lists every supported demo case.
func Cases() []string {
	return []string{CaseOK, CaseSplitStartCode, CaseCorruptIndex, CaseOBUTruncated, CaseKeyframeRegress, CaseBoundarySkew}
}

// ValidateCase rejects unknown case names.
func ValidateCase(c string) error {
	for _, k := range Cases() {
		if k == c {
			return nil
		}
	}
	return fmt.Errorf("sample: unknown case %q", c)
}

// DefaultLayers returns the required low/mid/high ladder. The archive is a
// legacy mix: low H.264, mid H.265 and high AV1.
func DefaultLayers(targets []float64) ([]LayerSpec, error) {
	if len(targets) != 3 {
		return nil, fmt.Errorf("sample: need exactly three target bitrates, got %d", len(targets))
	}
	for i, t := range targets {
		if !(t > 0) || math.IsInf(t, 0) || math.IsNaN(t) {
			return nil, fmt.Errorf("sample: target %d must be a positive number", i)
		}
	}
	return []LayerSpec{
		{Name: "low", Codec: codec.H264, TargetKbps: targets[0], Timescale: 30000},
		{Name: "mid", Codec: codec.H265, TargetKbps: targets[1], Timescale: 60000},
		{Name: "high", Codec: codec.AV1, TargetKbps: targets[2], Timescale: 90000},
	}, nil
}

// Generate builds a deterministic fixture for one case.
func Generate(c string, seed int64, chunkBytes int, targets []float64) (Archive, error) {
	if err := ValidateCase(c); err != nil {
		return Archive{}, err
	}
	if chunkBytes <= 0 {
		return Archive{}, errors.New("sample: chunk bytes must be positive")
	}
	specs, err := DefaultLayers(targets)
	if err != nil {
		return Archive{}, err
	}
	const durationMs = 1000
	a := Archive{Case: c, Seed: seed, ChunkBytes: chunkBytes, DurationMs: durationMs, frameLayout: map[string][]frameExtent{}}
	for i, spec := range specs {
		layer, err := buildLayer(spec, seed, i, chunkBytes, durationMs, c == CaseSplitStartCode && spec.Codec != codec.AV1)
		if err != nil {
			return Archive{}, err
		}
		a.Layers = append(a.Layers, layer)
		ext := make([]frameExtent, len(layer.frameOffsets))
		for j := range layer.frameOffsets {
			ext[j] = frameExtent{offset: layer.frameOffsets[j], length: layer.frameLengths[j]}
		}
		a.frameLayout[spec.Name] = ext
	}
	if err := applyCase(&a); err != nil {
		return Archive{}, err
	}
	return a, nil
}

// TargetBytes converts a target bitrate to the number of bytes needed for the
// given duration, rounding to the nearest byte.
func TargetBytes(kbps float64, durationMs int64) int64 {
	return int64(math.Round(kbps * float64(durationMs) / 8))
}

func buildLayer(spec LayerSpec, seed int64, layerIndex, chunkBytes int, durationMs int64, splitProbe bool) (Layer, error) {
	total := TargetBytes(spec.TargetKbps, durationMs)
	if total < 4096 {
		return Layer{}, fmt.Errorf("sample: target %s too small to build a valid stream", spec.Name)
	}
	bs := newBuilder(spec.Codec, prngSeed(seed, layerIndex), total)
	var leading []byte
	if splitProbe {
		leading = bs.splitFiller(chunkBytes)
	}
	res, err := bs.build(durationMs, NominalKeyframeIntervalMs, leading)
	if err != nil {
		return Layer{}, err
	}
	ix := index.Index{Layer: spec.Name, Timescale: spec.Timescale}
	for i := range res.offsets {
		if !res.keys[i] {
			continue
		}
		ix.Entries = append(ix.Entries, index.Entry{PTS: msToTicks(res.times[i], spec.Timescale), Offset: res.offsets[i], Line: len(ix.Entries) + 1})
	}
	return Layer{Name: spec.Name, Codec: spec.Codec, TargetKbps: spec.TargetKbps, Timescale: spec.Timescale,
		DurationMs: durationMs, Stream: res.stream, Index: ix,
		frameOffsets: append([]int64(nil), res.offsets...), frameLengths: append([]int64(nil), res.lengths...)}, nil
}

// msToTicks converts whole milliseconds to exact ticks for the given
// timescale; generated timescales all divide evenly for the 250 ms cadence.
func msToTicks(ms int64, timescale uint32) int64 {
	return ms * int64(timescale) / 1000
}

// applyCase injects the case-specific corruption. Media stays byte-identical
// except for the AV1 truncation; index corruption only rewrites index records.
func applyCase(a *Archive) error {
	switch a.Case {
	case CaseOK, CaseSplitStartCode:
		return nil
	case CaseCorruptIndex:
		l := layerByName(a, "mid")
		if l == nil {
			return errors.New("sample: missing mid layer")
		}
		if len(l.Index.Entries) < 3 {
			return errors.New("sample: corrupt-index needs three keyframe entries")
		}
		l.Index.Entries[2].Offset = int64(len(l.Stream)) + 64
	case CaseKeyframeRegress:
		l := layerByName(a, "low")
		if l == nil {
			return errors.New("sample: missing low layer")
		}
		if len(l.Index.Entries) < 3 {
			return errors.New("sample: keyframe-regress needs three keyframe entries")
		}
		// Move the third anchor back before the second, so the index time
		// order contradicts the stream order.
		regressed := msToTicks(100, l.Timescale)
		prev := l.Index.Entries[1].PTS
		if regressed >= prev {
			regressed = prev - msToTicks(50, l.Timescale)
		}
		l.Index.Entries[2].PTS = regressed
	case CaseBoundarySkew:
		l := layerByName(a, "mid")
		if l == nil {
			return errors.New("sample: missing mid layer")
		}
		shift := msToTicks(8, l.Timescale)
		for i := range l.Index.Entries {
			l.Index.Entries[i].PTS += shift
		}
	case CaseOBUTruncated:
		l := layerByName(a, "high")
		if l == nil {
			return errors.New("sample: missing high layer")
		}
		// Cut through the middle of the last coded frame. The frame's OBU
		// header and size field survive the cut, so the parser can prove the
		// declared payload extends past the available bytes.
		frames := a.frameLayout["high"]
		if len(frames) == 0 {
			return errors.New("sample: missing AV1 frame layout")
		}
		last := frames[len(frames)-1]
		if last.length < 64 {
			return errors.New("sample: final AV1 frame too small to truncate")
		}
		end := last.offset + last.length/2
		if end >= int64(len(l.Stream)) {
			return errors.New("sample: invalid AV1 truncation point")
		}
		l.Stream = l.Stream[:end]
	default:
		return fmt.Errorf("sample: unhandled case %q", a.Case)
	}
	return nil
}

func layerByName(a *Archive, name string) *Layer {
	for i := range a.Layers {
		if a.Layers[i].Name == name {
			return &a.Layers[i]
		}
	}
	return nil
}

// prngSeed mixes the fixture seed with the layer index deterministically.
func prngSeed(seed int64, layer int) int64 {
	x := uint64(seed)*0x9E3779B97F4A7C15 + uint64(layer+1)*0xBF58476D1CE4E5B9
	x ^= x >> 30
	x *= 0xBF58476D1CE4E5B9
	x ^= x >> 27
	return int64(x)
}
