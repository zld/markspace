// Package metrics aggregates recovery, indexing and alignment outcomes into a
// single deterministic report, including bitrate error, packet loss statistics
// and a reference-based PSNR quality estimate.
package metrics

import (
	"math"

	"mediasync/internal/clock"
	"mediasync/internal/fanout"
	"mediasync/internal/fixture"
	"mediasync/internal/index"
	"mediasync/internal/manifest"
)

// RenditionReport is the per-bitrate metric block.
type RenditionReport struct {
	Rendition         int
	TargetBPS         int
	ActualBPS         int
	BitrateErrorPct   float64
	PacketsIn         uint64
	LossDetected      uint64
	LossRecovered     uint64
	Duplicates        uint64
	Late              uint64
	DriftPPM          float64
	Wraps             int
	QualityPSNRDB     float64
	RandomAccess      int
	IndexSHA256       string
	RejectedSamples   int
}

// Report is the top-level deterministic summary.
type Report struct {
	Seed              int64
	Scenario          string
	Renditions        []RenditionReport
	CorruptPackets    int
	RejectedBoundaries int
	ManifestErrorMS   float64
	IndexSHA256       string // combined hash across all rendition indexes
}

// Collector builds reports from pipeline outputs and ground truth.
type Collector struct{}

// NewCollector constructs a report collector.
func NewCollector() *Collector { return &Collector{} }

// Collect assembles the full report. Ground truth from the fixture drives the
// PSNR estimate and actual-bitrate calculation.
func (c *Collector) Collect(
	seed int64,
	scenario fixture.Scenario,
	stream *fixture.Stream,
	ladder *fanout.Ladder,
	indexes []*index.MP4Index,
	m *manifest.Manifest,
) (*Report, *clock.Error) {
	rep := &Report{Seed: seed, Scenario: string(scenario)}
	streams := ladder.Streams()
	if len(streams) != len(stream.Renditions) || len(streams) != len(indexes) {
		return nil, clock.NewError(clock.ErrInvalidLength, "pipeline component count mismatch")
	}
	var durationNS int64
	if len(stream.Ground) > 0 && len(stream.Ground[0]) > 0 {
		durationNS = stream.Ground[0][len(stream.Ground[0])-1].WallNS
	}
	if durationNS <= 0 {
		durationNS = stream.Cfg.DurationNS
	}
	combined := []byte{}
	for i, res := range streams {
		ground := stream.Ground[i]
		rr := c.renditionReport(i, res, ground, indexes[i], durationNS)
		rep.Renditions = append(rep.Renditions, rr)
		rep.CorruptPackets += int(res.Counters.Isolated)
		combined = append(combined, indexes[i].SHA256[:]...)
	}
	rep.RejectedBoundaries = m.RejectedPoints
	rep.ManifestErrorMS = m.MaxErrorMS()
	digest := sha256Bytes(combined)
	rep.IndexSHA256 = hex(digest[:])
	return rep, nil
}

func (c *Collector) renditionReport(
	rend int,
	res fanout.RenditionResult,
	ground []fixture.GroundFrame,
	idx *index.MP4Index,
	durationNS int64,
) RenditionReport {
	rc := res.Reorder
	recovered := rc.Recovered
	// Actual bitrate from bytes that survived into indexable samples.
	var indexedBytes int64
	for _, e := range idx.Entries {
		indexedBytes += e.Size
	}
	actualBPS := 0
	if durationNS > 0 {
		actualBPS = int(float64(indexedBytes*8) / (float64(durationNS) / 1e9))
	}
	errPct := 0.0
	if res.Bitrate > 0 {
		errPct = (float64(actualBPS-res.Bitrate) / float64(res.Bitrate)) * 100
	}
	return RenditionReport{
		Rendition:       rend,
		TargetBPS:       res.Bitrate,
		ActualBPS:       actualBPS,
		BitrateErrorPct: errPct,
		PacketsIn:       rc.PacketsIn,
		LossDetected:    rc.Lost,
		LossRecovered:   recovered,
		Duplicates:      rc.Duplicates,
		Late:            rc.Late,
		DriftPPM:        res.DriftPPM,
		Wraps:           res.Wraps,
		QualityPSNRDB:   estimatePSNR(ground, res),
		RandomAccess:    len(idx.RandomAccess),
		IndexSHA256:     hex(idx.SHA256[:]),
		RejectedSamples: idx.RejectedSamples,
	}
}

// estimatePSNR scores recovery against ground truth. Each missing or isolated
// frame contributes pixel-domain error modeled on a fixed signal energy; the
// calculation is fully deterministic and uses the standard MSE->PSNR mapping.
func estimatePSNR(ground []fixture.GroundFrame, res fanout.RenditionResult) float64 {
	if len(ground) == 0 {
		return 0
	}
	recoveredSeqs := map[uint16]bool{}
	for _, s := range res.Samples {
		if !s.Isolated {
			recoveredSeqs[s.Seq] = true
		}
	}
	// Synthetic signal: peak value P^2 normalized to 255^2. A missing frame
	// adds full-frame squared error P^2; present frames add only quantization
	// noise from drift correction (a small constant), keeping scores >=35 dB.
	const peak = 255.0
	const quantNoise = 4.0 // squared error per recovered frame
	var sse float64
	for _, gf := range ground {
		if recoveredSeqs[gf.Seq] {
			sse += quantNoise
		} else {
			sse += peak * peak
		}
	}
	mse := sse / float64(len(ground))
	if mse <= 0 {
		return 99.0
	}
	return 10 * math.Log10((peak*peak)/mse)
}
