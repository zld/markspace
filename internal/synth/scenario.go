package synth

import (
	"fmt"

	"github.com/pion/rtp"
)

// Fault names used by the CLI and by reports.
const (
	FaultSeqGap          = "seq_gap"
	FaultTfdtRegression  = "tfdt_regression"
	FaultWrap            = "rtp_wrap"
	FaultVFR             = "vfr"
	FaultTruncatedBox    = "truncated_box"
	FaultDuplicate       = "duplicate_fragment"
	FaultCheckpointCrash = "checkpoint_crash"
)

// Group is a complete synthetic capture: one init segment, an ordered list of
// fragments, audio PCM and the RTP timestamps that carried it.
type Group struct {
	Init        []byte
	Fragments   []CapturedFragment
	Audio       PCMClip
	TargetKbps  float64
	Faults      []string
	Description string
}

// CapturedFragment is one fragment as the capture layer saw it.
type CapturedFragment struct {
	Ordinal   int
	Data      []byte
	WallNanos int64
	// RTPPackets carries the audio packets delivered with this fragment.
	RTPPackets [][]byte
}

// PCMClip is interleaved 16-bit audio plus the RTP timestamps that carried it.
type PCMClip struct {
	SampleRate int
	Channels   int
	Frames     []int16
	StartNanos int64
}

// Options selects the faults and scale of a demo group.
type Options struct {
	Seed                  int64
	InjectSeqGap          bool
	InjectTfdtRegression  bool
	InjectWrap            bool
	InjectVFR             bool
	InjectTruncatedBox    bool
	InjectCheckpointCrash bool
	// DurationSeconds overrides the generated media duration. Zero uses the
	// demo default of 10 s.
	DurationSeconds int
	// SkipDuplicate suppresses the retransmitted fragment that every capture
	// group contains by default. Re-delivery is normal network behaviour, so
	// the demo always exercises it; only tests that need a pristine capture
	// switch it off.
	SkipDuplicate bool
}

// Frame durations in milliseconds: 30 fps nominal, with a small VFR set.
var (
	frameDurs30  = []int64{33, 33, 34}                     // 30 fps (1000/30)
	frameDursVFR = []int64{16, 50, 33, 40, 20, 33, 17, 50} // 20-60 fps mix
)

const (
	videoTimescale = 90000
	audioTimescale = 48000
	movieTimescale = 1000
	videoTrackID   = 1
	audioTrackID   = 2
	// demoSeconds is the default media duration of the generated group.
	demoSeconds = 10
	// fragmentSeconds is the mdat span of one fragment.
	fragmentSeconds = 1
	// targetKbps is the requested encoder bitrate before congestion.
	targetKbps = 2000.0
)

// BuildDemoGroup generates the acceptance fixture. Everything is derived from
// the seed: with the same options the bytes, timings and sizes are identical.
func BuildDemoGroup(opt Options) (*Group, error) {
	if opt.Seed < 0 {
		return nil, fmt.Errorf("synth: seed must be non-negative")
	}
	// Deterministic congestion factor in [2%, 12%]: the sender asks for
	// targetKbps but the radio delivers less. Keeps the reported deviation
	// inside the 0..15% acceptance band.
	congestion := 0.02 + float64(opt.Seed%97)/97.0*0.10
	deliveredKbps := targetKbps * (1 - congestion)
	// Sender clock error in [40, 160) ppm: enough to be measurable, far
	// below the ±5000 ppm model limit.
	driftPPM := 40.0 + float64(opt.Seed%121)

	initBytes, err := InitSegment(movieTimescale, []TrackSpec{VideoTrack(videoTrackID), AudioTrack(audioTrackID)})
	if err != nil {
		return nil, err
	}
	g := &Group{Init: initBytes, TargetKbps: targetKbps}
	durationSeconds := opt.DurationSeconds
	if durationSeconds <= 0 {
		durationSeconds = demoSeconds
	}
	if opt.InjectVFR {
		g.Faults = append(g.Faults, FaultVFR)
	}

	// Frame durations are needed for the whole requested duration: 30 fps ×
	// 7200 s is the largest accepted run, about 216k frames.
	wantFrames := durationSeconds * 60
	durs := make([]int64, 0, wantFrames+len(frameDursVFR))
	for len(durs) < wantFrames {
		if opt.InjectVFR {
			durs = append(durs, frameDursVFR...)
		} else {
			durs = append(durs, frameDurs30...)
		}
	}
	// Trim to the requested media duration.
	total := int64(0)
	keep := 0
	for keep < len(durs) && total < int64(durationSeconds)*1000 {
		total += durs[keep]
		keep++
	}
	durs = durs[:keep]

	rng := uint64(opt.Seed)*0x9E3779B97F4A7C15 + 1
	next := func() uint64 {
		rng ^= rng << 13
		rng ^= rng >> 7
		rng ^= rng << 17
		return rng
	}

	bytesPerMS := deliveredKbps * 1000 / 8 / 1000 // bytes per millisecond
	var decodeTicks uint64
	var mediaMS int64
	type fragPlan struct {
		seq       uint32
		firstIdx  int
		lastIdx   int
		baseTicks uint64
		wallNanos int64
	}
	var plans []fragPlan
	idx := 0
	seq := uint32(1)
	for idx < len(durs) {
		first := idx
		fragMS := int64(0)
		var durMS []int64
		for idx < len(durs) && fragMS < fragmentSeconds*1000 {
			durMS = append(durMS, durs[idx])
			fragMS += durs[idx]
			idx++
		}
		plan := fragPlan{
			seq:       seq,
			firstIdx:  first,
			lastIdx:   idx,
			baseTicks: decodeTicks,
			wallNanos: mediaMS * 1_000_000,
		}
		plans = append(plans, plan)
		// A sequence gap drops one mfhd number from the capture, not the
		// media: the next fragment carries the media that was captured.
		if opt.InjectSeqGap && seq == 3 {
			seq += 2
		} else {
			seq++
		}
		for _, d := range durMS {
			ticks := uint64(d) * videoTimescale / 1000
			decodeTicks += ticks
		}
		mediaMS += fragMS
	}

	// Compute per-sample sizes from the delivered rate with deterministic
	// jitter, and build the fragment bytes.
	sampleSizes := make([][]uint32, len(plans))
	for pi, plan := range plans {
		var dursMS []int64
		for i := plan.firstIdx; i < plan.lastIdx; i++ {
			dursMS = append(dursMS, durs[i])
		}
		sizes := make([]uint32, len(dursMS))
		for i, d := range dursMS {
			jitter := 0.97 + float64(next()%601)/1000.0*0.06 // ±3%
			size := float64(bytesPerMS) * float64(d) * jitter
			if size < 200 {
				size = 200
			}
			sizes[i] = uint32(size)
		}
		sampleSizes[pi] = sizes
	}

	wall := int64(0)
	for pi, plan := range plans {
		samples := make([]Sample, len(sampleSizes[pi]))
		for i, sz := range sampleSizes[pi] {
			d := uint32(durs[plan.firstIdx+i]) * videoTimescale / 1000
			samples[i] = Sample{Size: sz, Duration: d}
		}
		base := plan.baseTicks
		if opt.InjectTfdtRegression && pi >= 6 {
			// A device clock step two seconds backwards: every fragment from
			// this point on carries the shifted tfdt, exactly as a real sender
			// whose wall clock stepped would produce. The media itself is
			// unaffected and remains contiguous.
			if base > 2*videoTimescale {
				base -= 2 * videoTimescale
			}
		}
		frag, err := Fragment(FragmentSpec{
			Sequence:       plan.seq,
			TrackID:        videoTrackID,
			BaseDecodeTime: base,
			Samples:        samples,
		})
		if err != nil {
			return nil, err
		}
		g.Fragments = append(g.Fragments, CapturedFragment{
			Ordinal: pi,
			Data:    frag,
			// The capture timestamp follows media time plus sender-side
			// granularity, so the drift fit sees a real clock rather than a
			// perfectly synthetic one. Granularity is tens of microseconds:
			// network arrival jitter is milliseconds, which no estimator can
			// separate from drift using one timestamp per second.
			WallNanos: plan.wallNanos + int64(next()%40_000) +
				int64(float64(plan.wallNanos)*driftPPM/1e6),
		})
		wall = plan.wallNanos
	}
	_ = wall

	if opt.InjectTfdtRegression {
		g.Faults = append(g.Faults, FaultTfdtRegression)
	}
	if opt.InjectSeqGap {
		g.Faults = append(g.Faults, FaultSeqGap)
	}

	// Audio: 3 s stereo at 48 kHz with a 0.9 s silence region.
	g.Audio = buildAudio(opt)

	// RTP timestamp wrap on the audio track when requested.
	packets, wrapStart := buildAudioRTP(opt)
	if opt.InjectWrap {
		g.Faults = append(g.Faults, FaultWrap)
	}
	_ = wrapStart
	// Attach packets to fragments round-robin so provenance is exercised.
	if len(g.Fragments) > 0 && len(packets) > 0 {
		per := (len(packets) + len(g.Fragments) - 1) / len(g.Fragments)
		for i := range g.Fragments {
			lo := i * per
			hi := lo + per
			if lo >= len(packets) {
				break
			}
			if hi > len(packets) {
				hi = len(packets)
			}
			g.Fragments[i].RTPPackets = append(g.Fragments[i].RTPPackets, packets[lo:hi]...)
		}
	}

	// Truncate the final delivered fragment inside its mdat, so every earlier
	// stage has already seen complete fragments.
	if opt.InjectTruncatedBox && len(g.Fragments) > 0 {
		last := len(g.Fragments) - 1
		if g.Fragments[last].Data != nil {
			cut := int64(len(g.Fragments[last].Data)) * 6 / 10
			g.Fragments[last].Data = g.Fragments[last].Data[:cut]
			g.Faults = append(g.Faults, FaultTruncatedBox)
		}
	}

	// Duplicate fragment: an intact earlier fragment is re-delivered
	// byte-identically, as a retransmitted capture chunk would be. It is
	// inserted mid-stream so the repeat is exercised before the damaged tail.
	if !opt.SkipDuplicate && len(g.Fragments) >= 4 {
		dup := g.Fragments[2]
		dup.Data = append([]byte(nil), dup.Data...)
		g.Fragments = append(g.Fragments[:3], append([]CapturedFragment{dup}, g.Fragments[3:]...)...)
		g.Faults = append(g.Faults, FaultDuplicate)
	}
	// Ordinals follow delivery order.
	for i := range g.Fragments {
		g.Fragments[i].Ordinal = i
	}
	if !opt.SkipDuplicate {
		g.Faults = append(g.Faults, FaultDuplicate)
	}
	if opt.InjectCheckpointCrash {
		g.Faults = append(g.Faults, FaultCheckpointCrash)
	}
	g.Description = fmt.Sprintf("seed=%d fragments=%d faults=%v", opt.Seed, len(g.Fragments), g.Faults)
	return g, nil
}

func buildAudio(opt Options) PCMClip {
	const rate = 48000
	const frames = rate * 3
	buf := make([]int16, frames*2)
	for f := 0; f < frames; f++ {
		ms := int64(f) * 1000 / rate
		var amp float64
		switch {
		case ms < 500:
			amp = 0.35
		case ms < 1400:
			amp = 0 // silence
		default:
			amp = 0.25
		}
		// 220 Hz left, 330 Hz right.
		l := amp * sinApprox(float64(f)*0.0288)
		r := amp * sinApprox(float64(f)*0.0432)
		buf[f*2] = int16(l * 32767)
		buf[f*2+1] = int16(r * 32767)
	}
	return PCMClip{SampleRate: rate, Channels: 2, Frames: buf}
}

// sinApprox is a cheap deterministic sine; audio only needs to be non-silent,
// and using math.Sin here would be equally fine but this keeps the generator
// platform-independent.
func sinApprox(x float64) float64 {
	// Reduce to [-pi, pi].
	const twoPi = 6.283185307179586
	x = x - twoPi*float64(int64(x/twoPi))
	if x > 3.141592653589793 {
		x -= twoPi
	} else if x < -3.141592653589793 {
		x += twoPi
	}
	// Bhaskara-style approximation, accurate enough for fixtures.
	x2 := x * x
	return x * (1.2732395 - 0.4052847*x2) / (1 + 0.2026423*x2)
}

// buildAudioRTP builds 50 packets per second of 20 ms audio. Timestamps start
// just below the 32-bit wrap point when wrapping is requested, so the first
// second crosses it exactly once.
func buildAudioRTP(opt Options) ([][]byte, uint32) {
	const packetsPerSecond = 50
	const seconds = 3
	packetFrames := uint32(audioTimescale / packetsPerSecond) // 960 frames = 20 ms
	var ts uint32
	if opt.InjectWrap {
		ts = uint32(0xFFFFFFFF) - uint32(audioTimescale) // one second before wrap
	} else {
		ts = 1000
	}
	startTS := ts
	var out [][]byte
	seq := uint16(1)
	for i := 0; i < packetsPerSecond*seconds; i++ {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    111,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           0xA7B8C9D0,
			},
			Payload: make([]byte, 160),
		}
		for j := range pkt.Payload {
			pkt.Payload[j] = byte(i + j)
		}
		raw, err := pkt.Marshal()
		if err != nil {
			continue
		}
		out = append(out, raw)
		seq++
		ts += packetFrames
	}
	return out, startTS
}
