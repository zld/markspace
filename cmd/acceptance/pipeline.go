package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"time"

	"github.com/pion/rtp"

	"mediacheck/internal/asset"
	"mediacheck/internal/budget"
	"mediacheck/internal/cache"
	"mediacheck/internal/derive"
	"mediacheck/internal/gap"
	"mediacheck/internal/manifest"
	"mediacheck/internal/metrics"
	rtpx "mediacheck/internal/rtp"
	"mediacheck/internal/track"
	"mediacheck/internal/wav"
)

type healthyOptions struct {
	caseName      string
	trackID       string
	outDir        string
	fixture       *pcmFixture
	arrivals      []*rtp.Packet
	bandwidthBPS  float64
	packetFrames  int
	segmentFrames int64
}

func runHealthy(opts healthyOptions) (Report, error) {
	rep := newReport(opts.caseName, opts.outDir)
	if err := mkdirAll(opts.outDir); err != nil {
		return rep, err
	}
	f := opts.fixture

	assets, err := asset.NewStore(filepath.Join(opts.outDir, "assets"))
	if err != nil {
		return rep, err
	}
	src, err := assets.Import(bytes.NewReader(f.WAV), int64(len(f.WAV)), "audio/wav")
	if err != nil {
		return rep, err
	}
	rep.SourceSHA = src.SHA256
	if err := cache.AtomicWriteFile(filepath.Join(opts.outDir, "source.wav"), f.WAV, 0o644); err != nil {
		return rep, err
	}

	wr, err := wav.OpenAt(bytes.NewReader(f.WAV), int64(len(f.WAV)))
	if err != nil {
		return rep, err
	}
	meta := wr.Meta()

	ledger := newGapLedger(opts.trackID, uint32(f.Rate))
	rb, err := rtpx.NewReorderer(rtpx.Config{
		Track: opts.trackID, SampleRate: uint32(f.Rate), StartSequence: 0,
		Window: 64, SamplesPerPacket: int64(opts.packetFrames), Ledger: ledger,
	})
	if err != nil {
		return rep, err
	}
	var timed []rtpx.TimedPacket
	duplicates, late := 0, 0
	for _, raw := range marshalPackets(opts.arrivals) {
		res, err := rb.PushRaw(raw)
		if err != nil {
			return rep, err
		}
		if res.Duplicate {
			duplicates++
		}
		if res.Late {
			late++
		}
		timed = append(timed, res.Ready...)
	}
	rest, err := rb.Flush()
	if err != nil {
		return rep, err
	}
	timed = append(timed, rest...)
	rep.GapCount = ledger.Count()
	if rep.GapCount != 0 {
		return rep, fmt.Errorf("healthy case produced %d gaps", rep.GapCount)
	}

	tr, err := track.New(opts.trackID, f.Rate, f.Ch, f.Bits, nil)
	if err != nil {
		return rep, err
	}
	tr.SourceSHA = src.SHA256
	tr.AddGaps(ledger.Gaps())
	blockAlign := int64(f.Ch * f.Bits / 8)
	for _, tp := range timed {
		offset := meta.DataOffset + tp.StartSample*blockAlign
		if err := tr.AppendPCM(tp.StartSample, tp.Packet.Payload, f.Bits, offset); err != nil {
			return rep, err
		}
	}
	if tr.Delivered != int64(f.Frames) {
		return rep, fmt.Errorf("delivered %d frames want %d", tr.Delivered, f.Frames)
	}

	bwWindow, err := budget.NewWindow(5 * time.Second)
	if err != nil {
		return rep, err
	}
	for i := 0; i < 3; i++ {
		if err := bwWindow.AddBPS(time.Duration(i)*time.Second, opts.bandwidthBPS); err != nil {
			return rep, err
		}
	}
	decision, err := budget.AdaptWindow(bwWindow, 2*time.Second, f.Rate, f.Ch)
	if err != nil {
		return rep, err
	}
	if !decision.Delivered {
		return rep, fmt.Errorf("budget did not select a tier: %s", decision.Reason)
	}
	rep.TargetBPS = decision.TargetBPS

	segments, err := derive.BuildAll(tr, decision.Bits, opts.segmentFrames)
	if err != nil {
		return rep, err
	}
	rep.Fragments = len(segments)
	rep.Segments = len(segments)

	mani := &manifest.Manifest{
		Case: opts.caseName, TrackID: opts.trackID, SourceSHA256: src.SHA256,
		ConfigSHA256: mustConfigDigest(map[string]any{
			"bits": decision.Bits, "segment_frames": opts.segmentFrames, "rate": f.Rate, "channels": f.Ch,
		}),
		SampleRate: f.Rate, Channels: f.Ch, Gaps: ledger.Gaps(),
	}
	var fragmentBytes int64
	var deliveredSamples []int32
	for i, seg := range segments {
		name := fmt.Sprintf("segment-%03d.m4s", i)
		path := filepath.Join(opts.outDir, name)
		if err := cache.AtomicWriteFile(path, seg.Data, 0o644); err != nil {
			return rep, err
		}
		fragmentBytes += int64(len(seg.Data))
		actual, err := metrics.ActualBitrate(int64(len(seg.Data)), seg.View.FrameCount, f.Rate)
		if err != nil {
			return rep, err
		}
		delta, err := metrics.DeltaPercent(decision.TargetBPS, actual)
		if err != nil {
			return rep, err
		}
		mani.Segments = append(mani.Segments, manifest.Segment{
			Index: i, TrackID: opts.trackID, File: name, FragmentSHA256: seg.SHA256,
			StartSample: seg.View.StartSample, EndSample: seg.View.StartSample + seg.View.FrameCount,
			StartTick: seg.View.StartTick, EndTick: seg.View.EndTick,
			SourceOffset: seg.SourceOffset, SourceLength: seg.SourceLength,
			OutputBits: decision.Bits, Channels: f.Ch, SampleRate: f.Rate,
			TargetBPS: decision.TargetBPS, ActualBPS: actual, DeltaPct: delta,
			PayloadBytes:   int64(seg.View.PayloadBytes),
			ContainerBytes: int64(len(seg.Data)),
			Delivered:      true,
			Gaps:           []gap.Gap{},
		})
	}
	for _, c := range tr.Chunks {
		deliveredSamples = append(deliveredSamples, c.Samples...)
	}
	reconstructed, err := metrics.QuantizeLeftAligned(deliveredSamples, decision.Bits)
	if err != nil {
		return rep, err
	}
	align := metrics.AlignAndSNR(f.Samples, reconstructed, 2)
	rep.SNRDB = align.SNRDB
	rep.AlignErrSamples = align.ErrorSamples
	rep.ActualBPS, err = metrics.ActualBitrate(fragmentBytes, int64(f.Frames), f.Rate)
	if err != nil {
		return rep, err
	}
	rep.DeltaPct, err = metrics.DeltaPercent(decision.TargetBPS, rep.ActualBPS)
	if err != nil {
		return rep, err
	}
	mani.Quality = manifest.Quality{
		TargetBPS: rep.TargetBPS, ActualBPS: rep.ActualBPS, DeltaPct: rep.DeltaPct,
		SNRDB: rep.SNRDB, AlignErrSamples: rep.AlignErrSamples, BadOffset: -1, GapCount: 0,
	}
	manifestPath := filepath.Join(opts.outDir, "manifest.json")
	if _, err := mani.WriteFile(manifestPath); err != nil {
		return rep, err
	}
	rep.Manifest = manifestPath
	rep.Details["ordered_packets"] = len(timed)
	rep.Details["duplicates"] = duplicates
	rep.Details["late"] = late
	rep.Details["output_bits"] = decision.Bits
	rep.Details["segment_files"] = len(segments)
	rep.OK = true
	return rep, nil
}
