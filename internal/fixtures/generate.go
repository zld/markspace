package fixtures

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediaarchive/internal/stream"
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
		gens = []genFunc{genNormal2997, genNormal25, genNormal50, genNormal24,
			genDrift, genVFR, genTruncated, genRTPWrap, genRTPLossy, genSubtitleBad}
	case SetNormal:
		gens = []genFunc{genNormal2997, genNormal25, genNormal50, genNormal24}
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

func genNormal50(dir string) (Asset, error) {
	sizes := typicalSizes(400, 7400)
	spec := mp4Spec(50000, 1000, len(sizes), sizes, 50)
	mp4Path, err := writeSetFile(dir, "normal_50.mp4", BuildMP4(spec))
	if err != nil {
		return Asset{}, err
	}
	srtPath, err := writeSetFile(dir, "normal_50.srt", []byte(FiftySRT()))
	if err != nil {
		return Asset{}, err
	}
	return Asset{
		Name: "normal_50", MP4: mp4Path, Subtitle: srtPath,
		TargetBps: targetFor(sizes, float64(len(sizes))*1000/50000), Tag: "normal",
		FPS: "50", LabelFormat: "50",
	}, nil
}

func genNormal24(dir string) (Asset, error) {
	sizes := typicalSizes(240, 7000)
	spec := mp4Spec(24000, 1000, len(sizes), sizes, 24)
	mp4Path, err := writeSetFile(dir, "normal_24.mp4", BuildMP4(spec))
	if err != nil {
		return Asset{}, err
	}
	srtPath, err := writeSetFile(dir, "normal_24.srt", []byte(TwentyFourSRT()))
	if err != nil {
		return Asset{}, err
	}
	return Asset{
		Name: "normal_24", MP4: mp4Path, Subtitle: srtPath,
		TargetBps: targetFor(sizes, float64(len(sizes))*1000/24000), Tag: "normal",
		FPS: "24", LabelFormat: "24",
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
	var pkts []*rtp.Packet
	for i := 0; i < 1000; i++ {
		pkts = append(pkts, &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96, SequenceNumber: uint16(65500 + i),
				Timestamp: 0xFFFF0000 + uint32(i)*3000, SSRC: 4242,
			},
			Payload: []byte{0x10},
		})
	}
	path := filepath.Join(dir, "wrap.rtp")
	if err := writeCapture(path, pkts); err != nil {
		return Asset{}, err
	}
	return Asset{Name: "rtp_wrap", RTP: path, Tag: "wrap"}, nil
}

func writeCapture(path string, pkts []*rtp.Packet) error {
	var b bytes.Buffer
	if err := stream.WriteRTPCapture(&b, pkts); err != nil {
		return err
	}
	return WriteFile(path, b.Bytes())
}

func genRTPLossy(dir string) (Asset, error) {
	next := func(i int) *rtp.Packet {
		return &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96, SequenceNumber: uint16(1000 + i),
				Timestamp: 9_000_000 + uint32(i)*3000, SSRC: 4242,
			},
			Payload: []byte{0x10},
		}
	}
	var pkts []*rtp.Packet
	jump := false
	for i := 0; i < 1200; i++ {
		if i >= 900 && !jump {
			jump = true
		}
		switch {
		case i >= 100 && i <= 104, i >= 400 && i <= 401:
			continue // packet loss
		case i == 600:
			p := next(i)
			pkts = append(pkts, p, p) // duplicate
			continue
		case i == 700:
			pkts = append(pkts, next(701)) // reorder pair
			continue
		case i == 701:
			pkts = append(pkts, next(700))
			continue
		}
		p := next(i)
		if jump {
			// A 5 s forward burst, far beyond any plausible frame interval.
			p.Timestamp += 90000 * 5
		}
		pkts = append(pkts, p)
	}
	path := filepath.Join(dir, "lossy.rtp")
	if err := writeCapture(path, pkts); err != nil {
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
