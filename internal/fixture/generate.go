// Package fixture deterministically generates MP4 sources, a camera manifest
// and RTP sidecars for the demo. Every byte derives from a seed, so output is
// identical on arm64 and amd64 and no external file is referenced.
package fixture

import (
	"math"
	"math/rand"
	"os"
	"path/filepath"

	mp4 "github.com/abema/go-mp4"
)

const SecondNs int64 = 1_000_000_000

// Spec controls one generated recording. The sample table may start at a
// nonzero stored media time (PressStartOffsetT) that the edit list maps to
// presentation zero, modelling a camera pressed before the session clock
// started. RecordingSec is the presented duration.
type Spec struct {
	Camera            string
	FileName          string
	Timescale         uint32
	FPS               float64
	PressStartOffsetT int64
	RecordingSec      float64
	KeyframeStep      int   // samples between sync samples
	WindowBytes       int64 // target payload bytes inside the recording window
	EditStartSec      float64
	EditEndSec        float64
	BytesPerSec       int
	Width             uint16
	Height            uint16
	Seed              int64
	MissingSync       bool
	EditOverrunMs     int64 // if >0, the presented elst duration is inflated
	// StuckTail makes the final N samples share one DTS (zero delta), which is
	// how a jittery capture card produces a regression. It sits at the tail so
	// earlier presentation times and the editorial window are intact.
	StuckTail int
}

// Sample is one sample-table entry.
type Sample struct {
	Offset uint64
	Size   uint32
	DTS    int64
	PTS    int64
	Sync   bool
	Delta  uint32
}

// Table is the complete metadata layout for one generated track.
type Table struct {
	Spec           Spec
	Samples        []Sample
	MediaTicks     int64
	EditMediaTime  int64
	EditMovieDur   uint64
	MovieTimescale uint32
	// PressStartMs is the empty edit preceding the runway: the time between
	// recording start and camera press.
	PressStartMs int64
	MdatOffset   uint64
	MdatSize     uint64
	FileSize     int64
}

// Frames returns the number of samples implied by the spec.
func (s Spec) Frames() int {
	n := int(math.Round(s.FPS * s.RecordingSec))
	if n < 2 {
		n = 2
	}
	return n
}

// Delta returns the per-sample DTS delta in media ticks.
func (s Spec) Delta() uint32 {
	d := int64(math.Round(float64(s.Timescale) / s.FPS))
	if d < 1 {
		d = 1
	}
	return uint32(d)
}

// buildSamples plans sizes, offsets and timing, normalizing payload inside the
// editorial window to WindowBytes so measured bitrate matches the target.
func buildSamples(s Spec, dataStart uint64) []Sample {
	rng := rand.New(rand.NewSource(s.Seed))
	n := s.Frames()
	delta := s.Delta()
	perFrame := float64(s.BytesPerSec) / s.FPS
	if perFrame < 64 {
		perFrame = 64
	}
	out := make([]Sample, n)
	off := dataStart
	for i := range out {
		size := uint32(math.Round(perFrame * (0.85 + 0.30*rng.Float64())))
		if size < 64 {
			size = 64
		}
		sync := i%s.KeyframeStep == 0
		if sync {
			size = uint32(float64(size) * 2.4)
		}
		cto := int64(0)
		if i%3 == 1 && i+1 < n {
			cto = int64(delta)
		}
		dts := int64(i) * int64(delta)
		out[i] = Sample{Offset: off, Size: size, DTS: dts, PTS: dts + cto, Sync: sync, Delta: delta}
		off += uint64(size)
	}
	if s.WindowBytes > 0 {
		normalizeWindow(out, s, delta)
		off = dataStart
		for i := range out {
			out[i].Offset = off
			off += uint64(out[i].Size)
		}
	}
	return out
}

// windowRange returns the half-open sample index range presented inside the
// editorial window. Sample i is presented at media tick i*delta, which maps
// through the edit list to (i*delta - PressStartOffsetT) ticks after the
// recording start.
func windowRange(s Spec, delta uint32) (lo, hi int) {
	startT := int64(s.EditStartSec*float64(s.Timescale)) + s.PressStartOffsetT
	endT := int64(s.EditEndSec*float64(s.Timescale)) + s.PressStartOffsetT
	lo, hi = -1, -1
	for i := 0; i < s.Frames(); i++ {
		p := int64(i) * int64(delta)
		if p >= startT && p < endT {
			if lo < 0 {
				lo = i
			}
			hi = i + 1
		}
	}
	return lo, hi
}

func normalizeWindow(samples []Sample, s Spec, delta uint32) {
	lo, hi := windowRange(s, delta)
	if lo < 0 || hi <= lo {
		return
	}
	var have uint64
	for _, smp := range samples[lo:hi] {
		have += uint64(smp.Size)
	}
	if have == 0 {
		return
	}
	factor := float64(s.WindowBytes) / float64(have)
	var got uint64
	for i := lo; i < hi; i++ {
		v := uint32(math.Round(float64(samples[i].Size) * factor))
		if v < 64 {
			v = 64
		}
		samples[i].Size = v
		got += uint64(v)
	}
	// Absorb rounding drift in the last sample while keeping the 64-byte floor.
	diff := int64(s.WindowBytes) - int64(got)
	last := &samples[hi-1]
	if diff > 0 {
		last.Size += uint32(diff)
	} else if diff < 0 {
		drop := uint32(-diff)
		if drop > last.Size-64 {
			drop = last.Size - 64
		}
		last.Size -= drop
	}
}

// Manifest mirrors the on-disk schema. Broken variants are produced only via
// deliberate corruption parameters, never by invalid JSON.
// video track per camera; RTP sidecars provide wrap evidence.
func syncSampleNumbers(samples []Sample) []uint32 {
	out := make([]uint32, 0, len(samples)/8+1)
	for i, s := range samples {
		if s.Sync {
			out = append(out, uint32(i+1))
		}
	}
	return out
}

func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// gridAlign rounds ns up to the next frame boundary for a camera whose first
// frame lands at base.
func gridAlign(ns, base int64, fps float64) int64 {
	period := int64(math.Round(float64(SecondNs) / fps))
	if period <= 0 {
		return ns
	}
	off := (ns - base) % period
	if off == 0 {
		return ns
	}
	if off < 0 {
		off += period
	}
	return ns + (period - off)
}

// sttsEntries builds timing runs, optionally making the final stuck samples
// share one DTS by using a zero delta.
func sttsEntries(total int, delta uint32, stuck int) []mp4.SttsEntry {
	if stuck <= 0 || stuck >= total {
		return []mp4.SttsEntry{{SampleCount: uint32(total), SampleDelta: delta}}
	}
	return []mp4.SttsEntry{
		{SampleCount: uint32(total - stuck), SampleDelta: delta},
		{SampleCount: uint32(stuck), SampleDelta: 0},
	}
}
