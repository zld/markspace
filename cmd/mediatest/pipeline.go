package main

import (
	"math"

	"medialib/internal/archive"
	"medialib/internal/audio"
	"medialib/internal/clock"
	"medialib/internal/fingerprint"
	"medialib/internal/ingest"
	"medialib/internal/reorder"
)

// report carries every metric the acceptance run prints.
type report struct {
	rate, channels int
	peakDBFS       float64
	alignError     int64
	segmentHash    string
	frameHash      string
	pxMin, pxMax   int
	pxMean, pxNZ   int
	duplicates     int
	lost           int
	limiter        bool
	reused         bool
	archiveHash    string // hash of the committed artifact bytes
}

// defaultSpec is the normal fixture: varying AAC/Opus-like frame
// durations, exactly one duplicate and two lost packets.
func defaultSpec() ingest.StreamSpec {
	return ingest.StreamSpec{
		ClockRate:    32000,
		Packets:      48,
		BaseSeq:      1000,
		BaseTS:       5000,
		FrameSamples: []uint32{640, 960, 512},
		DupSeq:       []int{7},
		DropSeq:      []int{12, 23},
		Seed:         0x5eed,
	}
}

// runPipeline executes the fixed chain: parse, reorder, synthesize
// PCM on the rational timeline, resample, map layout, control
// loudness, fingerprint, then journal and commit into the archive.
func runPipeline(archiveDir string, spec ingest.StreamSpec, p params) (*report, error) {
	rep := &report{rate: p.outRate, channels: p.channels}
	raw := ingest.GenRTP(spec)
	pkts := make([]ingest.Packet, 0, len(raw))
	for i, dat := range raw {
		pkt, err := ingest.ParseRTP(dat, i)
		if err != nil {
			return nil, err
		}
		pkts = append(pkts, pkt)
	}
	rr, err := reorder.Window{Size: 4}.Process(pkts, func(seq uint32) int64 {
		return spec.FrameDur(int(seq))
	})
	if err != nil {
		return nil, err
	}
	rep.duplicates, rep.lost = rr.Duplicates, rr.LostPackets

	// Total timeline length includes the explicit missing intervals.
	total := int64(0)
	for i := 0; i < spec.Packets; i++ {
		total += spec.FrameDur(i)
	}
	pcm := audio.PCM{Rate: int(spec.ClockRate), Channels: 1,
		Samples: make([]float32, total)} // gaps stay zero: missing, not shifted
	for _, pkt := range rr.Ordered {
		off := int64(pkt.Timestamp - spec.BaseTS)
		dur := spec.FrameDur(int(pkt.Seq - spec.BaseSeq))
		for n := int64(0); n < dur; n++ {
			pos := off + n
			pcm.Samples[pos] = float32(0.5 * math.Sin(2*math.Pi*440*float64(pos)/float64(spec.ClockRate)))
		}
	}

	out, err := audio.Resample(pcm, p.outRate)
	if err != nil {
		return nil, err
	}
	out, err = audio.MapChannels(out, p.channels)
	if err != nil {
		return nil, err
	}
	out, lst, err := audio.Normalize(out, p.targetRMS, p.peakCeil)
	if err != nil {
		return nil, err
	}
	rep.peakDBFS, rep.limiter = lst.PeakDBFS, lst.LimiterEngaged

	src, _ := clock.NewTimeline(int64(spec.ClockRate))
	dst, _ := clock.NewTimeline(int64(p.outRate))
	rep.alignError, err = clock.AlignError(src, total, dst, int64(out.Frames()))
	if err != nil {
		return nil, err
	}

	rep.segmentHash = fingerprint.Hash(out)
	store, err := archive.OpenStore(archiveDir)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	norm := fingerprint.Normalize(out)
	rep.archiveHash = fingerprint.HashBytes(norm)
	if rep.reused, err = store.Commit("audio", "segment.pcm", norm, rep.archiveHash); err != nil {
		return nil, err
	}

	px := renderWaveform(out, 320, 120)
	rep.frameHash = fingerprint.HashBytes(px)
	rep.pxMin, rep.pxMax, rep.pxMean, rep.pxNZ = pixelStats(px)
	return rep, nil
}

// renderWaveform draws an off-screen grayscale waveform, w by h,
// row-major. Deterministic for identical input.
func renderWaveform(p audio.PCM, w, h int) []byte {
	px := make([]byte, w*h)
	frames := p.Frames()
	for x := 0; x < w; x++ {
		f0 := x * frames / w
		f1 := (x + 1) * frames / w
		if f1 <= f0 {
			f1 = f0 + 1
		}
		lo, hi := float32(0), float32(0)
		for f := f0; f < f1 && f < frames; f++ {
			v := p.Samples[f*p.Channels]
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
		y0 := int((1 - float64(hi+1)/2) * float64(h-1))
		y1 := int((1 - float64(lo+1)/2) * float64(h-1))
		for y := y0; y <= y1 && y < h; y++ {
			px[y*w+x] = 255
		}
	}
	return px
}

func pixelStats(px []byte) (min, max, mean, nonzero int) {
	min, max = 255, 0
	sum := 0
	for _, v := range px {
		if int(v) < min {
			min = int(v)
		}
		if int(v) > max {
			max = int(v)
		}
		sum += int(v)
		if v != 0 {
			nonzero++
		}
	}
	return min, max, sum / len(px), nonzero
}
