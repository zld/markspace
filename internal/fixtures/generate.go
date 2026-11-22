package fixtures

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Asset is one generated fixture asset: any combination of MP4, subtitle and
// RTP log. TargetBps is the declared archival target used to score bitrate
// deviation; it is always derived from the generated payload, then rounded to
// a realistic 10 kbit/s step so the check is not trivially zero.
type Asset struct {
	Name        string
	MP4         string
	Subtitle    string
	RTP         string
	TargetBps   int64
	Tag         string
	FPS         string
	LabelFormat string
}

// Set is a generated fixture directory plus its assets in stable order.
type Set struct {
	Dir    string
	Assets []Asset
}

// Fixture set names accepted by the CLI.
const (
	SetAll      = "generated"
	SetNormal   = "normal"
	SetDamaged  = "damaged"
	SetDrift    = "drift"
	SetVFR      = "vfr"
	SetWrap     = "wrap"
	SetTrunc    = "truncated"
	SetRTP      = "rtp"
	SetSubtitle = "subtitle"
)

// Generate writes the requested fixture set into dir. Content is a pure
// function of the set name, so repeated generation yields identical bytes.
func Generate(dir, which string) (*Set, error) {
	if which == "" {
		which = SetAll
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	set := &Set{Dir: dir}
	type genFunc func(string) (Asset, error)
	var gens []genFunc
	switch which {
	case SetAll:
		gens = []genFunc{genNormal2997, genNormal25, genDrift, genVFR,
			genTruncated, genRTPWrap, genRTPLossy, genSubtitleBad}
	case SetNormal:
		gens = []genFunc{genNormal2997, genNormal25}
	case SetDamaged:
		gens = []genFunc{genTruncated, genRTPLossy, genSubtitleBad}
	case SetDrift:
		gens = []genFunc{genDrift}
	case SetVFR:
		gens = []genFunc{genVFR}
	case SetWrap:
		gens = []genFunc{genRTPWrap}
	case SetTrunc:
		gens = []genFunc{genTruncated}
	case SetRTP:
		gens = []genFunc{genRTPLossy}
	case SetSubtitle:
		gens = []genFunc{genSubtitleBad}
	default:
		return nil, fmt.Errorf("unknown fixture set %q", which)
	}
	for _, gen := range gens {
		a, err := gen(dir)
		if err != nil {
			return nil, err
		}
		set.Assets = append(set.Assets, a)
	}
	return set, nil
}

// typicalSizes produces a deterministic, mildly varying sample size pattern.
func typicalSizes(n int, base uint32) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		out[i] = base + uint32(i*137)%800
	}
	return out
}

func targetFor(sizes []uint32, seconds float64) int64 {
	var total int64
	for _, s := range sizes {
		total += int64(s)
	}
	bps := float64(total*8) / seconds
	// Round to 10 kbit/s and never below 1.
	rounded := int64(bps/10000+0.5) * 10000
	if rounded < 10000 {
		rounded = 10000
	}
	return rounded
}

func mp4Spec(timescale uint32, delta uint32, n int, sizes []uint32, gop int) MP4Spec {
	spec := MP4Spec{Timescale: timescale}
	for i := 0; i < n; i++ {
		d := delta
		spec.Samples = append(spec.Samples, MP4Sample{Size: sizes[i], Delta: d, IsSync: i%gop == 0})
	}
	return spec
}

func writeSetFile(dir, name string, data []byte) (string, error) {
	path := filepath.Join(dir, name)
	return path, WriteFile(path, data)
}

func genNormal2997(dir string) (Asset, error) {
	sizes := typicalSizes(300, 8000)
	spec := mp4Spec(90000, 3003, len(sizes), sizes, 30)
	mp4Path, err := writeSetFile(dir, "normal_2997.mp4", BuildMP4(spec))
	if err != nil {
		return Asset{}, err
	}
	srtPath, err := writeSetFile(dir, "normal_2997.srt", []byte(NormalSRT()))
	if err != nil {
		return Asset{}, err
	}
	return Asset{
		Name: "normal_2997", MP4: mp4Path, Subtitle: srtPath,
		TargetBps: targetFor(sizes, float64(len(sizes))*3003/90000), Tag: "normal",
		FPS: "30000/1001", LabelFormat: "29.97df",
	}, nil
}

func genNormal25(dir string) (Asset, error) {
	sizes := typicalSizes(250, 7600)
	spec := mp4Spec(25000, 1000, len(sizes), sizes, 25)
	mp4Path, err := writeSetFile(dir, "normal_25.mp4", BuildMP4(spec))
	if err != nil {
		return Asset{}, err
	}
	srtPath, err := writeSetFile(dir, "normal_25.srt", []byte(MultiRateSRT()))
	if err != nil {
		return Asset{}, err
	}
	return Asset{
		Name: "normal_25", MP4: mp4Path, Subtitle: srtPath,
		TargetBps: targetFor(sizes, float64(len(sizes))*1000/25000), Tag: "normal",
		FPS: "25", LabelFormat: "25",
	}, nil
}

func genDrift(dir string) (Asset, error) {
	sizes := typicalSizes(300, 8200)
	spec := mp4Spec(90000, 3004, len(sizes), sizes, 30)
	mp4Path, err := writeSetFile(dir, "drift_2997.mp4", BuildMP4(spec))
	if err != nil {
		return Asset{}, err
	}
	srtPath, err := writeSetFile(dir, "drift_2997.srt", []byte(DriftSRT()))
	if err != nil {
		return Asset{}, err
	}
	return Asset{
		Name: "drift_2997", MP4: mp4Path, Subtitle: srtPath,
		TargetBps: targetFor(sizes, float64(len(sizes))*3004/90000), Tag: "drift",
		FPS: "30000/1001",
	}, nil
}

func genVFR(dir string) (Asset, error) {
	sizes := typicalSizes(240, 7900)
	spec := MP4Spec{Timescale: 90000}
	for i := 0; i < len(sizes); i++ {
		delta := uint32(3003)
		switch {
		case i == 120:
			delta = 9009 // three-frame hold
		case i == 180:
			delta = 1001 // short interval: genuine VFR speed-up
		}
		spec.Samples = append(spec.Samples, MP4Sample{Size: sizes[i], Delta: delta, IsSync: i%24 == 0})
	}
	mp4Path, err := writeSetFile(dir, "vfr_hold.mp4", BuildMP4(spec))
	if err != nil {
		return Asset{}, err
	}
	vttPath, err := writeSetFile(dir, "vfr_hold.vtt", []byte(NormalVTT()))
	if err != nil {
		return Asset{}, err
	}
	var ticks int64
	for _, s := range spec.Samples {
		ticks += int64(s.Delta)
	}
	return Asset{
		Name: "vfr_hold", MP4: mp4Path, Subtitle: vttPath,
		TargetBps: targetFor(sizes, float64(ticks)/90000), Tag: "vfr",
		FPS: "30000/1001",
	}, nil
}

func genTruncated(dir string) (Asset, error) {
	sizes := typicalSizes(260, 8100)
	spec := mp4Spec(90000, 3003, len(sizes), sizes, 26)
	data := BuildMP4(spec)
	// Cut inside the payload so both the mdat extent and individual sample
	// extents exceed the real file end.
	cut := data[:len(data)*3/5]
	mp4Path, err := writeSetFile(dir, "truncated.mp4", cut)
	if err != nil {
		return Asset{}, err
	}
	srtPath, err := writeSetFile(dir, "truncated.srt", []byte(TruncatedSRT()))
	if err != nil {
		return Asset{}, err
	}
	return Asset{
		Name: "truncated", MP4: mp4Path, Subtitle: srtPath,
		TargetBps: targetFor(sizes, float64(len(sizes))*3003/90000), Tag: "truncated",
		FPS: "30000/1001",
	}, nil
}

func genRTPWrap(dir string) (Asset, error) {
	var b strings.Builder
	b.WriteString("# rtp wrap fixture: sequence and 32-bit timestamp both wrap\n")
	for i := 0; i < 1000; i++ {
		seq := (65500 + i) & 0xFFFF
		ts := uint32(0xFFFF0000 + uint32(i)*3000)
		fmt.Fprintf(&b, "%d %d 4242 96\n", seq, ts)
	}
	path, err := writeSetFile(dir, "wrap.rtp", []byte(b.String()))
	if err != nil {
		return Asset{}, err
	}
	return Asset{Name: "rtp_wrap", RTP: path, Tag: "wrap"}, nil
}

func genRTPLossy(dir string) (Asset, error) {
	var b strings.Builder
	b.WriteString("# rtp lossy fixture: gaps, late fills, duplicate, reorder, timestamp jump\n")
	next := func(i int) (uint16, uint32) {
		base := uint16(1000)
		seq := base + uint16(i)
		ts := uint32(9_000_000 + i*3000)
		return seq, ts
	}
	jump := false
	for i := 0; i < 1200; i++ {
		if i >= 900 && !jump {
			jump = true
			// Emit a burst that advances media time by 500 ms at once.
			seq, ts := next(i)
			fmt.Fprintf(&b, "%d %d 4242 96\n", seq, ts+90000*5)
			continue
		}
		switch {
		case i >= 100 && i <= 104, i >= 400 && i <= 401:
			continue // packet loss
		case i == 600:
			seq, ts := next(i)
			fmt.Fprintf(&b, "%d %d 4242 96\n", seq, ts)
			fmt.Fprintf(&b, "%d %d 4242 96\n", seq, ts) // duplicate
			continue
		case i == 700:
			seq, ts := next(701)
			fmt.Fprintf(&b, "%d %d 4242 96\n", seq, ts) // reorder pair
			continue
		case i == 701:
			seq, ts := next(700)
			fmt.Fprintf(&b, "%d %d 4242 96\n", seq, ts)
			continue
		}
		seq, ts := next(i)
		if jump {
			ts += 90000 * 5
		}
		fmt.Fprintf(&b, "%d %d 4242 96\n", seq, ts)
	}
	path, err := writeSetFile(dir, "lossy.rtp", []byte(b.String()))
	if err != nil {
		return Asset{}, err
	}
	return Asset{Name: "rtp_lossy", RTP: path, Tag: "rtp"}, nil
}

func genSubtitleBad(dir string) (Asset, error) {
	path, err := writeSetFile(dir, "bad_cues.srt", []byte(BadSRT()))
	if err != nil {
		return Asset{}, err
	}
	return Asset{Name: "subtitle_bad", Subtitle: path, Tag: "subtitle"}, nil
}
