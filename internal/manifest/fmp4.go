package manifest

import (
	"os"

	"github.com/abema/go-mp4"

	"rtpregress/internal/clock"
)

// audioTargetRate is the canonical sample rate. It mirrors audio.TargetRate;
// the package test asserts the two constants never drift apart.
const audioTargetRate = 48000

// FMp4Timing is the timing information read back from a fragmented MP4 file.
type FMp4Timing struct {
	Timescale       uint32   // mdhd timescale (expect 48000)
	Duration        uint64   // mdhd duration in timescale units
	FragmentBase    []uint64 // each tfdt baseMediaDecodeTime
	FragmentSamples []uint32 // each trun sample count
}

// ValidateFMp4 opens a fragmented MP4 and verifies its timing boxes against
// the canonical 48 kHz timescale. It returns the extracted timing for further
// cross-checking against the manifest timeline.
func ValidateFMp4(path string) (*FMp4Timing, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, clock.New(clock.CodeManifest, "manifest:fmp4", -1, 0, 0,
			"open %s: %v", path, err)
	}
	defer f.Close()

	t := &FMp4Timing{}
	paths := []mp4.BoxPath{
		// media header: moov/trak/mdia/mdhd
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		// per-fragment decode time and sample tables, any moof index
		{mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTfdt()},
		{mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTrun()},
	}
	boxes, err := mp4.ExtractBoxesWithPayload(f, nil, paths)
	if err != nil {
		return nil, clock.New(clock.CodeManifest, "manifest:fmp4", -1, 0, 0,
			"parse %s: %v", path, err)
	}
	for _, b := range boxes {
		switch p := b.Payload.(type) {
		case *mp4.Mdhd:
			t.Timescale = p.Timescale
			if p.Version == 1 {
				t.Duration = p.DurationV1
			} else {
				t.Duration = uint64(p.DurationV0)
			}
		case *mp4.Tfdt:
			if p.Version == 1 {
				t.FragmentBase = append(t.FragmentBase, p.BaseMediaDecodeTimeV1)
			} else {
				t.FragmentBase = append(t.FragmentBase, uint64(p.BaseMediaDecodeTimeV0))
			}
		case *mp4.Trun:
			t.FragmentSamples = append(t.FragmentSamples, p.SampleCount)
		}
	}
	if t.Timescale != uint32(audioTargetRate) {
		return t, clock.New(clock.CodeManifest, "manifest:fmp4", -1, 0, 0,
			"timescale %d != %d", t.Timescale, audioTargetRate)
	}
	if err := t.checkMonotonic(); err != nil {
		return t, err
	}
	return t, nil
}

// checkMonotonic enforces strictly non-decreasing fragment decode times and
// that every fragment declares at least one sample.
func (t *FMp4Timing) checkMonotonic() error {
	var last uint64
	for i, base := range t.FragmentBase {
		if i > 0 && base < last {
			return clock.New(clock.CodeManifest, "manifest:fmp4", -1, 0, 0,
				"tfdt went backwards at fragment %d: %d < %d", i, base, last)
		}
		last = base
	}
	for i, n := range t.FragmentSamples {
		if n == 0 {
			return clock.New(clock.CodeManifest, "manifest:fmp4", -1, 0, 0,
				"trun fragment %d declares zero samples", i)
		}
	}
	return nil
}

// MaxTimelineErrorMS compares each fragment's tfdt base (in 48 kHz ticks)
// against the expected first sample and returns the largest error in ms.
func (t *FMp4Timing) MaxTimelineErrorMS(expectedBases []int64) float64 {
	maxErr := 0.0
	for i, want := range expectedBases {
		if i >= len(t.FragmentBase) {
			break
		}
		deltaSamples := int64(t.FragmentBase[i]) - want
		if deltaSamples < 0 {
			deltaSamples = -deltaSamples
		}
		ms := float64(deltaSamples) * 1000.0 / float64(audioTargetRate)
		if ms > maxErr {
			maxErr = ms
		}
	}
	return maxErr
}
