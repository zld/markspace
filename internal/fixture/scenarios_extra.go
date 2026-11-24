package fixture

import (
	"fmt"
	"path/filepath"
)

func genDiscontinuity(dir string, gt *GroundTruth) error {
	const segSamples, gop = 30, 15
	var body string
	seg := 0
	for period := 0; period < 2; period++ {
		for k := 0; k < 2; k++ {
			rel := fmt.Sprintf("disc/seg-%d.mp4", seg)
			deltas := make([]uint32, segSamples)
			for i := range deltas {
				deltas[i] = frameDelta
			}
			if _, err := writeProgressive(filepath.Join(dir, rel), mediaTimescale, deltas, 2048, gop, uint64(300+seg)); err != nil {
				return err
			}
			if period == 1 && k == 0 {
				body += "#EXT-X-DISCONTINUITY\n"
			}
			body += fmt.Sprintf("#EXTINF:%.6f,\n%s\n", float64(segSamples*frameDelta)/mediaTimescale, rel)
			seg++
		}
	}
	pl := hlsHeader(2) + body + "#EXT-X-ENDLIST\n"
	gt.DiscontinuityManifest = filepath.Join(dir, "discontinuity.m3u8")
	return writeBytes(gt.DiscontinuityManifest, []byte(pl))
}

func genVFR(dir string, gt *GroundTruth) error {
	// Three GOPs of ten samples each with different real frame durations.
	deltas := []uint32{}
	for i := 0; i < 10; i++ {
		deltas = append(deltas, 3000)
	}
	for i := 0; i < 10; i++ {
		deltas = append(deltas, 2000)
	}
	for i := 0; i < 10; i++ {
		deltas = append(deltas, 4000)
	}
	sync := map[int]bool{0: true, 10: true, 20: true}
	specs := make([]SampleSpec, len(deltas))
	for i, d := range deltas {
		specs[i] = SampleSpec{Delta: d, Size: 1024 + (i%4)*64, Sync: sync[i]}
	}
	built, err := BuildProgressive(ProgressiveSpec{Timescale: mediaTimescale, Seed: 777, Samples: specs})
	if err != nil {
		return err
	}
	rel := "vfr/seg-0.mp4"
	if err := writeBytes(filepath.Join(dir, rel), built.Bytes); err != nil {
		return err
	}
	pl := hlsHeader(2) + fmt.Sprintf("#EXTINF:%.6f,\n%s\n",
		float64(totalDelta(deltas))/mediaTimescale, rel) + "#EXT-X-ENDLIST\n"
	gt.VFRManifest = filepath.Join(dir, "vfr.m3u8")
	return writeBytes(gt.VFRManifest, []byte(pl))
}

func totalDelta(d []uint32) uint64 {
	var s uint64
	for _, v := range d {
		s += uint64(v)
	}
	return s
}

func genWrapDrift(dir string, gt *GroundTruth) error {
	init := BuildFragmentedInit(mediaTimescale)
	if err := writeBytes(filepath.Join(dir, "wd/init.mp4"), init); err != nil {
		return err
	}

	const frags, samplesPer = 4, 30
	base0 := uint32((uint64(1) << 32) - 120000)
	var body string
	for j := 0; j < frags; j++ {
		rel := fmt.Sprintf("wd/seg-%d.m4s", j)
		deltas := make([]uint32, samplesPer)
		sizes := make([]uint32, samplesPer)
		for i := 0; i < samplesPer; i++ {
			deltas[i] = frameDelta
			sizes[i] = 256 + uint32((i%3)*64)
		}
		// +45 ticks per fragment => 90045 vs nominal 90000 => +500 ppm.
		base := base0 + uint32(j)*90045
		frag := BuildMediaFragment(FragmentSpec{
			Sequence: uint32(j + 1), BaseTime: base,
			Deltas: deltas, Sizes: sizes, Seed: uint64(900 + j),
		})
		if err := writeBytes(filepath.Join(dir, rel), frag); err != nil {
			return err
		}
		body += "#EXTINF:1.000000,\n" + rel + "\n"
	}
	pl := hlsHeader(2) + `#EXT-X-MAP:URI="wd/init.mp4"` + "\n" + body + "#EXT-X-ENDLIST\n"
	gt.WrapDriftManifest = filepath.Join(dir, "wrap_drift.m3u8")
	return writeBytes(gt.WrapDriftManifest, []byte(pl))
}
