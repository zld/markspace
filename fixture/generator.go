// Package fixture generates deterministic, self-contained sample material.
// No external media files are referenced: every WAV block is synthesized from
// a seeded PRNG so repeated runs produce byte-identical inputs.
package fixture

import (
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"

	"medpipe/audio"
	"medpipe/timeline"
)

// Spec configures one generated fixture set.
type Spec struct {
	Seed        int64
	Rate        uint32
	Channels    int
	Layout      string
	Blocks      int
	BlockFrames int
	Bits        uint16
	OutDir      string
	// Fault selects an injected defect; "" means a healthy set.
	Fault string
}

// Descriptor is the on-disk description of one generated block.
type Descriptor struct {
	TrackID     string
	Sequence    int
	StartSample int64
	Frames      int
	Path        string
	RTPSequence uint16
	DependsOn   []string
}

// Set is the full generated material plus metadata for the runner.
type Set struct {
	Spec       Spec
	Clock      timeline.Clock
	Tracks     []*timeline.Track
	Files      []Descriptor
	FaultBlock *Descriptor
}

// Fault names accepted by the generator.
const (
	FaultTruncated   = "truncated"
	FaultMissing     = "missing"
	FaultMisaligned  = "misaligned"
	FaultSeqJump     = "seqjump"
	FaultBadLength   = "badlength"
)

// AllFaults lists the five required defect classes.
func AllFaults() []string {
	return []string{FaultTruncated, FaultMissing, FaultMisaligned, FaultSeqJump, FaultBadLength}
}

// Track returns a generated track by id.
func (s *Set) Track(id string) *timeline.Track {
	for _, tr := range s.Tracks {
		if tr.ID == id {
			return tr
		}
	}
	return nil
}

// FilesOf returns the generated file descriptors of one track.
func (s *Set) FilesOf(id string) []Descriptor {
	var out []Descriptor
	for _, d := range s.Files {
		if d.TrackID == id {
			out = append(out, d)
		}
	}
	return out
}

// Missing reports gap errors on a track, empty when the coverage is complete.
func (s *Set) Missing(id string) []*timeline.Error {
	tr := s.Track(id)
	if tr == nil {
		return nil
	}
	var errs []*timeline.Error
	for _, g := range tr.Gaps() {
		errs = append(errs, timeline.NewError(timeline.ErrCodeMissingBlock,
			timeline.StageTimeline, id, "gap of %d samples in [%d,%d)",
			g.Length(), g.Start, g.End))
	}
	return errs
}

// Generate writes a fixture set to disk and returns its description.
func Generate(spec Spec) (*Set, error) {
	if spec.Blocks == 0 {
		spec.Blocks = 10
	}
	if spec.BlockFrames == 0 {
		spec.BlockFrames = 4800 // 100 ms at 48 kHz
	}
	if spec.Bits == 0 {
		spec.Bits = 16
	}
	clock, err := timeline.NewClock(spec.Rate)
	if err != nil {
		return nil, err
	}
	if _, err = audio.LookupLayout(spec.Layout); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(spec.OutDir, 0o755); err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewSource(spec.Seed))
	set := &Set{Spec: spec, Clock: clock}
	expected := timeline.Interval{Start: 0, End: int64(spec.Blocks * spec.BlockFrames)}
	set.Tracks = []*timeline.Track{
		{ID: "up", Rate: spec.Rate, Channels: uint16(spec.Channels), Layout: spec.Layout, Expected: expected},
		{ID: "down", Rate: spec.Rate, Channels: uint16(spec.Channels),
			Layout: spec.Layout, DependsOn: []string{"up"}, Expected: expected},
	}
	for _, tr := range set.Tracks {
		if err := writeTrack(set, rng, tr); err != nil {
			return nil, err
		}
	}
	return set, nil
}

func writeTrack(set *Set, rng *rand.Rand, tr *timeline.Track) error {
	spec := set.Spec
	for b := 0; b < spec.Blocks; b++ {
		start := int64(b * spec.BlockFrames)
		frames := spec.BlockFrames
		// Missing upstream block: nothing is written or appended, leaving a
		// real timeline gap that the track gap detector reports.
		if tr.ID == "up" && spec.Fault == FaultMissing && b == 3 {
			continue
		}
		// Misaligned timestamp: block 4 starts before block 3's start, after
		// three valid blocks have already committed checkpoints.
		actualStart := start
		if tr.ID == "up" && spec.Fault == FaultMisaligned && b == 4 {
			actualStart = start - int64(frames) - int64(frames)/4
		}
		samples := synth(rng, frames*spec.Channels)
		pcm, err := audio.EncodePCM(samples, spec.Bits)
		if err != nil {
			return err
		}
		path := filepath.Join(spec.OutDir, tr.ID+"_"+strconv.Itoa(b)+".wav")
		hdr := audio.WAVHeader{SampleRate: spec.Rate, Channels: uint16(spec.Channels), BitsPerSample: spec.Bits}
		if err := writeWAV(path, hdr, samples); err != nil {
			return err
		}
		// Truncation: cut the data payload short after a valid container write.
		if tr.ID == "up" && (spec.Fault == FaultTruncated || spec.Fault == FaultBadLength) && b == 3 {
			info, _ := os.Stat(path)
			cut := int64(7)
			if spec.Fault == FaultBadLength {
				cut = 1 // one byte breaks the frame grid
			}
			if err := os.Truncate(path, info.Size()-cut); err != nil {
				return err
			}
		}
		rtpSeq := uint16(b * 2)
		if tr.ID == "up" && spec.Fault == FaultSeqJump && b == 4 {
			rtpSeq = uint16(b*2) + 32 // jump larger than the validator threshold
		}
		desc := Descriptor{
			TrackID: tr.ID, Sequence: b, StartSample: actualStart, Frames: frames,
			Path: path, RTPSequence: rtpSeq, DependsOn: tr.DependsOn,
		}
		set.Files = append(set.Files, desc)
		if tr.ID == "up" && spec.Fault != "" && (b == 3 || b == 4) {
			d := desc
			set.FaultBlock = &d
		}
		tr.Blocks = append(tr.Blocks, timeline.Block{
			Sequence: b,
			Interval: timeline.Interval{Start: actualStart, End: actualStart + int64(frames)},
			PayloadBytes: len(pcm), RTPSequence: rtpSeq,
		})
	}
	return nil
}

func writeWAV(path string, h audio.WAVHeader, samples []float64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return audio.WriteWAV(f, h, samples)
}

// synth produces a deterministic tonal signal with a noise floor, giving the
// loudness meter real energy while staying below full scale.
func synth(rng *rand.Rand, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		phase := float64(i) * 2 * math.Pi * 440 / 48000
		out[i] = 0.45*math.Sin(phase) + (rng.Float64()-0.5)*0.05
	}
	return out
}
