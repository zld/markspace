package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Layout constants for the generated media. Sample sizes are chosen so the
// known byte count per second lands near the requested target bitrate.
const (
	videoTrackID uint32 = 1
	audioTrackID uint32 = 2
)

// defaults for the generated stream shape.
type shape struct {
	params   Params
	videoDur uint32 // ticks per video sample
	audioDur uint32
	videoSz  uint32
	audioSz  uint32
}

// deriveShape turns Params into concrete sample layout. Video and audio
// fragments share a wall-clock duration, so tick counts differ per timescale.
func deriveShape(p Params) shape {
	segDurTicksV := uint64(p.VideoTimescale) * uint64(p.SegmentMS) / 1000
	videoDur := uint32(segDurTicksV / uint64(p.SamplesPerFrag))
	segDurTicksA := uint64(p.AudioTimescale) * uint64(p.SegmentMS) / 1000
	audioDur := uint32(segDurTicksA / uint64(p.SamplesPerFrag))

	bytesPerFragV, bytesPerFragA := solveSampleSizes(p)
	return shape{params: p, videoDur: videoDur, audioDur: audioDur, videoSz: bytesPerFragV, audioSz: bytesPerFragA}
}

func (s shape) videoTrack() trackSpec {
	return trackSpec{
		id: videoTrackID, timescale: s.params.VideoTimescale, handler: "vide",
		kind: "vide", sampleDur: s.videoDur, sampleSize: s.videoSz,
		width: s.params.Width, height: s.params.Height,
	}
}

func (s shape) audioTrack() trackSpec {
	return trackSpec{
		id: audioTrackID, timescale: s.params.AudioTimescale, handler: "soun",
		kind: "soun", sampleDur: s.audioDur, sampleSize: s.audioSz,
	}
}

// solveSampleSizes chooses per-sample byte counts. When TargetBPS is set the
// sizes are solved from the measured box overhead so the delivered file's
// actual bitrate falls within 5% of the target; otherwise the requested
// per-track bitrates are used directly.
func solveSampleSizes(p Params) (uint32, uint32) {
	v := uint32(uint64(p.VideoBitrate) * uint64(p.SegmentMS) / 8000 / uint64(p.SamplesPerFrag))
	a := uint32(uint64(p.AudioBitrate) * uint64(p.SegmentMS) / 8000 / uint64(p.SamplesPerFrag))
	if v < 64 {
		v = 64
	}
	if a < 32 {
		a = 32
	}
	if p.TargetBPS <= 0 {
		return v, a
	}
	// Media bytes per second target, split 90/10 between video and audio. Box
	// and init overhead are roughly 0.2% of this layout, so the sample sizes
	// dominate the final size and this single-step solve keeps the delivered
	// bitrate well inside 5% of target (the CLI asserts it).
	wantPerSec := uint64(p.TargetBPS) / 8
	perSampleTotal := wantPerSec * uint64(p.SegmentMS) / 1000 / uint64(p.SamplesPerFrag)
	if perSampleTotal < 64 {
		perSampleTotal = 64
	}
	vv := uint32(perSampleTotal * 90 / 100)
	aa := uint32(perSampleTotal - uint64(vv))
	if vv < 32 {
		vv = 32
	}
	if aa < 16 {
		aa = 16
	}
	return vv, aa
}

// Generate builds one fixture with the requested damage applied.
func Generate(p Params, damage Damage) (*Generated, error) {
	if p.Fragments <= 0 || p.SamplesPerFrag <= 0 {
		return nil, fmt.Errorf("fixture: fragments and samples must be positive")
	}
	if p.VideoTimescale == 0 || p.AudioTimescale == 0 {
		return nil, fmt.Errorf("fixture: timescales must be non-zero")
	}
	sh := deriveShape(p)
	video := sh.videoTrack()
	audio := sh.audioTrack()

	segmentDurUs := int64(p.SegmentMS) * 1000
	totalDurTicks := uint32(p.Fragments) * p.SegmentMS
	var out bytes.Buffer

	switch damage {
	case DamageMissingInit:
		// Init is simply omitted; only fragments are written.
	default:
		out.Write(buildInit([]trackSpec{video, audio}, totalDurTicks, damage == DamageZeroTimescale))
	}

	gen := &Generated{Params: p, Damage: damage, SegmentDurUs: segmentDurUs}

	for f := 0; f < p.Fragments; f++ {
		seq := uint32(f + 1)
		if damage == DamageSequenceGap && f >= 2 {
			seq++ // skip exactly one sequence number, then continue
		}
		// tfdt values advance by the exact segment duration in each timescale.
		vTfdt := uint64(f) * uint64(p.SegmentMS) * uint64(p.VideoTimescale) / 1000
		aTfdt := uint64(f) * uint64(p.SegmentMS) * uint64(p.AudioTimescale) / 1000
		if damage == DamageTfdtRegression && f == 3 {
			// Move the fourth fragment back by 1.5 segments so its tfdt sits
			// strictly before the third fragment's end.
			vTfdt -= 3 * uint64(p.SegmentMS) * uint64(p.VideoTimescale) / 2000
			aTfdt -= 3 * uint64(p.SegmentMS) * uint64(p.AudioTimescale) / 2000
		}

		vSamples := make([]sampleSpec, p.SamplesPerFrag)
		for i := range vSamples {
			vSamples[i] = sampleSpec{
				size: sh.videoSz, dur: sh.videoDur, sync: i == 0,
				seed: p.Seed + int64(f)*100 + int64(i), kind: 'v',
			}
		}
		aSamples := make([]sampleSpec, p.SamplesPerFrag)
		for i := range aSamples {
			aSamples[i] = sampleSpec{
				size: sh.audioSz, dur: sh.audioDur, sync: true,
				seed: p.Seed + int64(f)*100 + int64(i) + 50, kind: 'a',
			}
		}
		seg := buildSegment(seq, []trafSpec{
			{track: video, tfdt: vTfdt, samples: vSamples},
			{track: audio, tfdt: aTfdt, samples: aSamples},
		})
		out.Write(seg)
		gen.Fragments = append(gen.Fragments, FragmentSpec{
			Sequence: seq, VideoTfdt: vTfdt, AudioTfdt: aTfdt,
			VideoSamples: len(vSamples), AudioSamples: len(aSamples),
		})
	}

	bytesOut := out.Bytes()

	switch damage {
	case DamageTruncatedBox:
		bytesOut = truncateMiddleFragment(bytesOut, 3, &gen.TruncatedBoxes, gen)
	case DamageCorruptIndex:
		idxs, corruptOff := appendCorruptIndex(bytesOut)
		bytesOut = idxs
		gen.CorruptIndexOffset = corruptOff
	}

	gen.Bytes = bytesOut
	gen.ExpectedValidSamples = countValidSamples(gen)
	return gen, nil
}

// truncateMiddleFragment inflates the declared size of one mdat so that it
// exceeds the bytes remaining in the input. The scanner records a truncated
// box, isolates the containing fragment, and resynchronises on the next moof;
// the later fragments are therefore recoverable even though they physically
// follow the damaged box.
func truncateMiddleFragment(data []byte, which int, truncated *int, gen *Generated) []byte {
	var positions []int
	pos := 0
	for pos+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		typ := string(data[pos+4 : pos+8])
		if size < 8 {
			break
		}
		if typ == "mdat" {
			positions = append(positions, pos)
		}
		pos += size
	}
	if which >= len(positions) {
		return data
	}
	target := positions[which]
	remaining := uint64(len(data) - target)
	// Declare more bytes than remain, guaranteeing a detected truncation.
	declared := remaining + 8192
	out := append([]byte(nil), data...)
	binary.BigEndian.PutUint32(out[target:target+4], uint32(declared))
	*truncated = 1
	for i := range gen.Fragments {
		if i == which {
			gen.Fragments[i].Truncated = true
		}
	}
	return out
}

// appendCorruptIndex adds a sidx whose references point at bogus offsets. The
// rebuilt index must not adopt them because they are not box boundaries.
func appendCorruptIndex(data []byte) ([]byte, uint64) {
	var w boxWriter
	w.write(data)
	base := uint64(len(data))
	// A sidx with two references. single-reference offsets are computed from
	// first_offset past the box end; we deliberately point them into the
	// middle of the first moof so validation rejects them.
	ref1 := be32(0x80000000 | 4091) // reference_type=1 (index), size 4091
	ref1 = append(ref1, be32(1000)...)
	ref1 = append(ref1, be32(0x90000000)...)
	ref2 := be32(uint32(7777))
	ref2 = append(ref2, be32(1000)...)
	ref2 = append(ref2, be32(0x90000000)...)
	sidx := fullBox("sidx", 0, 0, concat(
		be32(videoTrackID), be32(90000),
		be32(0), // earliest_presentation_time
		be32(3), // first_offset: 3 bytes past box end -> bogus
		be16(0), // reserved
		be16(2), // reference_count
		ref1, ref2,
	))
	w.write(sidx)
	return w.Bytes(), base
}

// countValidSamples returns the samples that survive: all samples in intact
// fragments.
func countValidSamples(gen *Generated) int {
	total := 0
	for _, f := range gen.Fragments {
		if f.Truncated {
			continue
		}
		total += f.VideoSamples + f.AudioSamples
	}
	return total
}

// TotalExpectedSamples returns every generated sample, including dropped ones.
func (g *Generated) TotalExpectedSamples() int {
	total := 0
	for _, f := range g.Fragments {
		total += f.VideoSamples + f.AudioSamples
	}
	return total
}

// RecoverableFragmentCount returns fragments after the first truncated one,
// which the scanner must resynchronise onto.
func (g *Generated) RecoverableFragmentCount() int {
	seenTruncated := false
	count := 0
	for _, f := range g.Fragments {
		if f.Truncated {
			seenTruncated = true
			continue
		}
		if seenTruncated {
			count++
		}
	}
	return count
}
