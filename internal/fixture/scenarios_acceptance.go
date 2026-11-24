package fixture

import (
	"fmt"
	"path/filepath"

	"mediascan/internal/clock"
)

const mediaTimescale = 90000
const frameDelta = 3000 // 29.97fps-ish

func hlsHeader(target int) string {
	return "#EXTM3U\n#EXT-X-VERSION:7\n" +
		fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", target) +
		"#EXT-X-PLAYLIST-TYPE:VOD\n"
}

func genGood(dir string, gt *GroundTruth) error {
	const segs, samples, gop = 4, 384, 48
	var body string
	for s := 0; s < segs; s++ {
		rel := fmt.Sprintf("good/seg-%d.mp4", s)
		deltas := make([]uint32, samples)
		for i := range deltas {
			deltas[i] = frameDelta
		}
		if _, err := writeProgressive(filepath.Join(dir, rel), mediaTimescale, deltas, 8192, gop, uint64(100+s)); err != nil {
			return err
		}
		body += fmt.Sprintf("#EXTINF:%.6f,\n%s\n", float64(samples*frameDelta)/mediaTimescale, rel)
	}
	pl := hlsHeader(13) + body + "#EXT-X-ENDLIST\n"
	gt.GoodManifest = filepath.Join(dir, "good.m3u8")
	return writeBytes(gt.GoodManifest, []byte(pl))
}

func genCorrupt(dir string, gt *GroundTruth) error {
	const segs, samples, gop = 4, 512, 64
	victimSeg := 2
	victimSample := 300

	var body string
	for s := 0; s < segs; s++ {
		rel := fmt.Sprintf("corrupt/seg-%d.mp4", s)
		deltas := make([]uint32, samples)
		for i := range deltas {
			deltas[i] = frameDelta
		}
		path := filepath.Join(dir, rel)
		if s == victimSeg {
			built, err := BuildProgressive(ProgressiveSpec{
				Timescale: mediaTimescale, Seed: uint64(200 + s),
				Samples: specSamples(deltas, 16384, gop),
			})
			if err != nil {
				return err
			}
			vs := built.Samples[victimSample]
			cut := vs.Offset + int64(vs.Size)/2
			if err := writeBytes(path, built.Bytes[:cut]); err != nil {
				return err
			}
			gt.Corrupt = CorruptTruth{
				Manifest:          filepath.Join(dir, "corrupt.m3u8"),
				Segment:           rel,
				CutOffsetBytes:    cut,
				OriginalSizeBytes: len(built.Bytes),
				VictimSampleIndex: victimSample,
				SampleStartOffset: vs.Offset,
				Timescale:         mediaTimescale,
				SampleTimeUnified: clock.MustUnified(vs.TimeStartTicks, mediaTimescale),
			}
		} else {
			if _, err := writeProgressive(path, mediaTimescale, deltas, 16384, gop, uint64(200+s)); err != nil {
				return err
			}
		}
		body += fmt.Sprintf("#EXTINF:%.6f,\n%s\n", float64(samples*frameDelta)/mediaTimescale, rel)
	}
	pl := hlsHeader(18) + body + "#EXT-X-ENDLIST\n"
	gt.CorruptManifest = filepath.Join(dir, "corrupt.m3u8")
	gt.Corrupt.Manifest = gt.CorruptManifest
	return writeBytes(gt.CorruptManifest, []byte(pl))
}

func specSamples(deltas []uint32, size int, gop int) []SampleSpec {
	out := make([]SampleSpec, len(deltas))
	for i, d := range deltas {
		out[i] = SampleSpec{Delta: d, Size: size + (i%5)*128, Sync: gop > 0 && i%gop == 0}
	}
	return out
}
