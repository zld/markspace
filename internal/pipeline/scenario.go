package pipeline

import (
	"sort"

	"github.com/pion/rtp"

	"example.com/mediaforge/internal/clock"
	"example.com/mediaforge/internal/fixture"
	"example.com/mediaforge/internal/merrors"
	"example.com/mediaforge/internal/syncer"
)

// ScenarioConfig drives the deterministic fixture generator. The demo passes
// its command-line values straight through.
type ScenarioConfig struct {
	Seed     int64
	Loss     int    // number of single-packet, parity-protected losses
	Reorder  int    // number of packets delivered one slot late
	DriftPPM int    // device clock drift to simulate, parts per million
	WrapAt   uint32 // raw timestamp at which the video clock wraps
	VFR      bool   // variable frame durations
	Segments int

	VideoPackets int
	AudioPackets int

	// BadRTCP injects an implausible Sender Report. TruncatedSample also
	// builds the truncated container fixture.
	BadRTCP         bool
	TruncatedSample bool

	// LateBy is how many packet slots the late packet is delayed, which must
	// exceed the reorder window for it to be reported as late.
	LateBy int
}

// DefaultScenarioConfig returns a demo-sized scenario. Loss=3 matches the
// documented acceptance command: a three-packet burst that parity cannot
// repair, alongside exactly one repairable single-packet loss.
func DefaultScenarioConfig() ScenarioConfig {
	return ScenarioConfig{
		Seed: 41, Loss: 3, Reorder: 7, DriftPPM: 120,
		WrapAt: 4294967000, VFR: true, Segments: 12,
		VideoPackets: 240, AudioPackets: 300,
		BadRTCP: true, TruncatedSample: true,
		LateBy: 7,
	}
}

// Scenario is a generated input plus the facts the demo reports.
type Scenario struct {
	Input             Input
	Config            ScenarioConfig
	ExpectedRecovered int
	ExpectedWraps     int
	ExpectedTruncated int
}

// Layout of the deterministic scenario. Sequence numbers are globally unique:
// video takes 1000.., audio 2000.., and parity replicas reuse a group's first
// media sequence number, distinguished by payload type.
const (
	videoSeqStart = uint16(1000)
	audioSeqStart = uint16(2000)
	videoSSRC     = uint32(0xA1B2C3D4)
	audioSSRC     = uint32(0x11223344)
	videoPayload  = uint8(96)
	audioPayload  = uint8(111)

	// audioStartTick places audio 40 ms into the shared clock: 3600 ticks at
	// the 90 kHz reference rate.
	audioStartRefTicks = int64(3600)
)

// Loss plan for the default scenario. Index k is the k-th video packet.
const (
	singleLossIdx = 10  // recovered from parity, always exactly one
	burstStartIdx = 100 // start of the unrecoverable consecutive burst
	lateIdx       = 150 // arrives outside the reorder window
	reorderStride = 13  // packets at multiples of this stride arrive one slot late
)

// Generate builds a complete, deterministic scenario.
func Generate(cfg ScenarioConfig) (*Scenario, error) {
	if cfg.VideoPackets < 40 || cfg.AudioPackets < 8 {
		return nil, merrors.New(merrors.StagePipeline, merrors.Invalid,
			"scenario needs at least 40 video and 8 audio packets")
	}
	if cfg.Segments <= 0 || cfg.Segments > 64 {
		return nil, merrors.New(merrors.StagePipeline, merrors.Invalid,
			"segments must be 1..64")
	}
	if cfg.Loss < 0 || cfg.Reorder < 0 || cfg.LateBy < 0 {
		return nil, merrors.New(merrors.StagePipeline, merrors.Invalid,
			"loss, reorder, and late-by must be non-negative")
	}
	if cfg.DriftPPM < -100_000 || cfg.DriftPPM > 100_000 {
		return nil, merrors.New(merrors.StagePipeline, merrors.OutOfRange,
			"drift %d ppm outside +-100000", cfg.DriftPPM)
	}
	if cfg.VideoPackets <= lateIdx+2 || cfg.VideoPackets <= burstStartIdx+cfg.Loss+2 {
		return nil, merrors.New(merrors.StagePipeline, merrors.Invalid,
			"video packet count %d too small for the loss plan", cfg.VideoPackets)
	}
	if cfg.Loss < 1 || cfg.Loss > 8 {
		return nil, merrors.New(merrors.StagePipeline, merrors.Invalid,
			"loss burst %d outside 1..8", cfg.Loss)
	}

	mp4, err := fixture.BuildMP4(fixture.MP4Options{
		VideoSamples: cfg.VideoPackets,
		AudioSamples: cfg.AudioPackets,
	})
	if err != nil {
		return nil, err
	}
	trunc, err := fixture.BuildMP4(fixture.MP4Options{
		VideoSamples: cfg.VideoPackets,
		AudioSamples: cfg.AudioPackets,
		TruncateLast: 25,
	})
	if err != nil {
		return nil, err
	}

	sc := &Scenario{Config: cfg}
	sc.Input = Input{
		MP4:                mp4,
		TruncatedMP4:       trunc,
		ReorderWindow:      4,
		MaxBuffer:          512,
		JitterWindowTicks:  9000,
		SegmentCount:       cfg.Segments,
		MaxCorrectionTicks: 9000,
		ReferenceRate:      90000,
		FECGroupSize:       4,
	}

	// Deterministic loss plan. Exactly one single-packet loss is protected by
	// parity and is recoverable. The burst is placed inside one parity group
	// and its parity packet is withheld, so it is unrecoverable no matter how
	// long it is. The late packet arrives but outside the reorder window.
	missing := map[int]bool{singleLossIdx: true, lateIdx: true}
	for i := 0; i < cfg.Loss; i++ {
		missing[burstStartIdx+i] = true
	}
	// The burst's group parity is dropped so the group cannot be repaired.
	burstGroup := burstStartIdx / sc.Input.FECGroupSize
	sc.ExpectedRecovered = 1

	// Timestamps, drift-corrected integer arithmetic. The video clock starts
	// two packets before WrapAt, so exactly one 32-bit wrap occurs.
	tickAt := func(base uint64, idx int, step uint32) uint32 {
		adv := uint64(idx) * uint64(step) * uint64(1_000_000+cfg.DriftPPM) / 1_000_000
		return uint32(base + adv)
	}
	// Start five packets before the wrap so the wrap happens after the
	// timeline is seeded with several packets: reordered packets then still
	// extend forward and land correctly.
	videoBase := uint64(cfg.WrapAt) - 5*3000
	videoTS := func(i int) uint32 { return tickAt(videoBase, i, 3000) }
	audioTS := func(i int) uint32 { return tickAt(1024, i, 1024) }

	// Arrival times in 90 kHz network ticks. Video and audio interleave.
	type wire struct {
		stream  int
		index   int
		seq     uint16
		ts      uint32
		payload []byte
		arrival uint64
		parity  bool
		group   int
	}
	var wires []wire
	videoSamples := make([][]byte, cfg.VideoPackets)
	for i := range videoSamples {
		videoSamples[i] = fixture.SamplePayload(fixture.VideoTrackID, i, 40+i%7)
	}
	audioSamples := make([][]byte, cfg.AudioPackets)
	for i := range audioSamples {
		audioSamples[i] = fixture.SamplePayload(fixture.AudioTrackID, i, 12+i%3)
	}

	// Frames carry timestamp and length so a repaired packet lands in the
	// right place with the right payload size.
	frames := make([][]byte, cfg.VideoPackets)
	for i := range frames {
		f, err := Frame(videoTS(i), videoSamples[i])
		if err != nil {
			return nil, err
		}
		frames[i] = f
	}

	var netTick uint64 = 100
	var videoArrivals []uint64
	for i := 0; i < cfg.VideoPackets; i++ {
		videoArrivals = append(videoArrivals, netTick)
		netTick += 3000
	}
	for i := 0; i < cfg.VideoPackets; i++ {
		// The late packet is not lost: it arrives, just too late for the
		// reorder window, so the pipeline must report it as late.
		if missing[i] && i != lateIdx {
			continue
		}
		delay := uint64(0)
		if i == lateIdx {
			// LateBy packets' worth of delay plus slack guarantees the
			// window has closed. The reorder window is 4 packets.
			delay = uint64(cfg.LateBy+1)*3000 + 3500
		} else if cfg.Reorder > 0 && i%reorderStride == 0 && i <= cfg.Reorder*reorderStride {
			// One slot late: the successor arrives first.
			delay = 3100
		}
		wires = append(wires, wire{
			stream: 0, index: i, seq: videoSeqStart + uint16(i), ts: videoTS(i),
			payload: videoSamples[i], arrival: videoArrivals[i] + delay,
		})
	}
	var audioNet uint64 = 200
	for i := 0; i < cfg.AudioPackets; i++ {
		wires = append(wires, wire{
			stream: 1, index: i, seq: audioSeqStart + uint16(i), ts: audioTS(i),
			payload: audioSamples[i], arrival: audioNet,
		})
		audioNet += 1920
	}
	// Duplicates: two delivered video packets arrive again 50 ticks later.
	dups := 0
	for i := 0; i < cfg.VideoPackets && dups < 2; i++ {
		if missing[i] || i == lateIdx {
			continue
		}
		wires = append(wires, wire{
			stream: 0, index: i, seq: videoSeqStart + uint16(i), ts: videoTS(i),
			payload: videoSamples[i], arrival: videoArrivals[i] + 50,
		})
		dups++
	}
	// Parity for every video group except the one whose parity is dropped.
	groupSize := sc.Input.FECGroupSize
	for g := 0; g*groupSize < cfg.VideoPackets; g++ {
		if g == burstGroup {
			// The burst's parity is withheld: this group is unrecoverable.
			continue
		}
		start, end := g*groupSize, (g+1)*groupSize
		if end > cfg.VideoPackets {
			end = cfg.VideoPackets
		}
		var members [][]byte
		for i := start; i < end; i++ {
			members = append(members, frames[i])
		}
		par, err := xorFrames(members)
		if err != nil {
			return nil, err
		}
		// Parity arrives after the group's last member had a chance to.
		lastArrival := videoArrivals[end-1]
		wires = append(wires, wire{
			stream: 0, seq: videoSeqStart + uint16(start), group: g,
			parity: true, payload: par, arrival: lastArrival + 2,
		})
	}

	sort.SliceStable(wires, func(a, b int) bool {
		if wires[a].arrival != wires[b].arrival {
			return wires[a].arrival < wires[b].arrival
		}
		return wires[a].seq < wires[b].seq
	})

	// Every video media packet carries its FEC frame, so the recovery cache
	// sees equal-length members. Audio is unprotected and unframed.
	videoFrames := make(map[int][]byte, len(frames))
	for i, f := range frames {
		videoFrames[i] = f
	}
	var videoPackets, audioPackets []RawPacket
	for _, w := range wires {
		pt := videoPayload
		ssrc := videoSSRC
		payload := w.payload
		if w.parity {
			pt = ParityPayloadType
		} else if w.stream == 1 {
			pt = audioPayload
			ssrc = audioSSRC
		} else {
			payload = videoFrames[w.index]
		}
		raw, err := marshalRTP(ssrc, pt, w.seq, w.ts, payload)
		if err != nil {
			return nil, err
		}
		p := RawPacket{Arrival: w.arrival, Bytes: raw}
		if w.stream == 0 {
			videoPackets = append(videoPackets, p)
		} else {
			audioPackets = append(audioPackets, p)
		}
	}

	// The video timeline is normalised onto the shared clock; audio is offset
	// 40 ms later, which is the inter-track skew the aligner corrects.
	sc.Input.Tracks = []TrackInput{
		{Kind: syncer.KindVideo, SSRC: videoSSRC, ClockRate: 90000,
			OffsetTicks: -int64(videoBase), MP4TrackID: fixture.VideoTrackID,
			StartSeq: videoSeqStart, Packets: videoPackets},
		{Kind: syncer.KindAudio, SSRC: audioSSRC, ClockRate: 48000,
			OffsetTicks: audioStartRefTicks, MP4TrackID: fixture.AudioTrackID,
			StartSeq: audioSeqStart, Packets: audioPackets},
	}

	// Sender Reports: drift-consistent, one per second, with a single
	// implausible jump that the estimator must quarantine.
	ntpBase := uint64(1_700_000_000_000_000)
	for i := 0; i < 6; i++ {
		ntp := ntpBase + uint64(i)*1_000_000
		rtpAt := uint64(4_000_000) + uint64(i)*(90000+90000*uint64(cfg.DriftPPM)/1_000_000)
		if cfg.BadRTCP && i == 3 {
			ntp -= 999_000
			rtpAt += 45_000
		}
		sc.Input.SenderReports = append(sc.Input.SenderReports, SRInput{
			Report: clock.NewSenderReport(videoSSRC, ntp, uint32(rtpAt)),
			RTPExt: rtpAt,
		})
	}

	sc.ExpectedWraps = 1
	if cfg.TruncatedSample {
		sc.ExpectedTruncated = 1
	}
	return sc, nil
}

func xorFrames(members [][]byte) ([]byte, error) {
	if len(members) == 0 {
		return nil, merrors.New(merrors.StageFEC, merrors.Empty, "no members")
	}
	out := make([]byte, len(members[0]))
	for _, m := range members {
		if len(m) != len(out) {
			return nil, merrors.New(merrors.StageFEC, merrors.Malformed, "member length")
		}
		for i := range out {
			out[i] ^= m[i]
		}
	}
	return out, nil
}

func marshalRTP(ssrc uint32, pt uint8, seq uint16, ts uint32, payload []byte) ([]byte, error) {
	var h rtp.Header
	h.Version = 2
	h.PayloadType = pt
	h.SequenceNumber = seq
	h.Timestamp = ts
	h.SSRC = ssrc
	raw, err := h.Marshal()
	if err != nil {
		return nil, merrors.Wrap(merrors.StageRTPParse, merrors.Malformed, 0, err, "fixture marshal")
	}
	return append(raw, payload...), nil
}
