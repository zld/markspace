package main

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"github.com/pion/rtp"

	"mediad/internal/clock"
	"mediad/internal/manifest"
	"mediad/internal/samplegen"
	"mediad/internal/stream"
)

// bytesReader keeps the io import surface explicit.
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// timeSecond is one second, used to convert the duration/segment durations.
func timeSecond() time.Duration { return time.Second }

// Logical-time pump parameters (no wall clock, fully deterministic).
const (
	packetsPerSec = 50
	// slowDrainEvery: the slow consumer services one packet only on steps
	// where step%slowDrainEvery==0, so its service rate is 1/slowDrainEvery
	// of the ingress rate and the bounded queue drops the bounded surplus.
	slowDrainEvery = 4
)

// stageStreamAndManifest pushes each layer's deterministic RTP trace through
// the bounded fan-out using an integer logical-time pump: every logical step
// produces one packet and a consumer services the queue. The slow tier skips
// service on a fixed step pattern so its bounded queue drops a bounded
// fraction of non-key packets; keyframes always get room and are retained.
func stageStreamAndManifest(cfg *demoConfig, masterTS uint32, segmentTicks, durationTicks int64,
) ([]manifest.LayerInput, int, error) {
	nLayers := len(cfg.bitrates)
	cfgs := make([]stream.LayerConfig, nLayers)
	for i, br := range cfg.bitrates {
		cfgs[i] = stream.LayerConfig{
			Name: fmt.Sprintf("%dk", br/1000), TargetBPS: br,
			QueueCap: cfg.queueCap, Policy: stream.PolicyDrop,
		}
	}
	fan, err := stream.NewFanout(cfgs)
	if err != nil {
		return nil, 0, err
	}
	defer fan.Close()

	durationSec := int(cfg.duration / timeSecond())
	nPackets := durationSec * packetsPerSec
	rtpClockRate := uint32(90000)
	step := rtpClockRate / packetsPerSec

	lateKeys := make([]map[int]bool, nLayers)
	for i := range lateKeys {
		lateKeys[i] = map[int]bool{}
	}
	lateLayer := nLayers - 1
	if cfg.faults["late-keyframe"] {
		lateIdx := int(cfg.segment/timeSecond()) * packetsPerSec * 2
		lateKeys[lateLayer][lateIdx] = true
	}

	// Per-layer deterministic phase offset in master ticks (0,5,10ms) makes
	// the cross-layer boundary alignment error measurable but bounded.
	phaseTicks := make([]int64, nLayers)
	for li := range phaseTicks {
		phaseTicks[li] = int64(li) * int64(masterTS) * 5 / 1000
	}

	unwrappers := make([]*clock.Unwrapper, nLayers)
	converters := make([]*clock.Converter, nLayers)
	traces := make([][]*rtp.Packet, nLayers)
	collected := make([][]stream.Envelope, nLayers)
	for li := range unwrappers {
		unwrappers[li] = clock.NewRTPUnwrapper()
		converters[li], err = clock.NewConverter(rtpClockRate, masterTS)
		if err != nil {
			return nil, 0, err
		}
		traces[li] = buildTrace(li, rtpClockRate, step, nPackets)
	}

	slowLayer := 1
	for pi := 0; pi < nPackets; pi++ {
		for li := 0; li < nLayers; li++ {
			pkt := traces[li][pi]
			expanded, _ := unwrappers[li].Next(pkt.Timestamp)
			master, _ := converters[li].Push(expanded)
			master += phaseTicks[li]

			if cfg.faults["late-keyframe"] && li == lateLayer && lateKeys[li][pi] {
				continue // withhold the boundary keyframe past the wait budget
			}
			// A keyframe always makes room: the consumer services the queue
			// before it is offered, so key packets never block or drop.
			if pkt.Marker {
				serviceOnce(fan, li, &collected[li])
			}
			env := stream.Envelope{
				Seq: pkt.SequenceNumber, TS: master, Key: pkt.Marker,
				Layer: li, Bytes: cfgs[li].TargetBPS / (8 * packetsPerSec),
			}
			if perr := fan.Publish(env); perr != nil {
				return nil, 0, perr
			}
			// Consumer service. Normal layers fully drain each step; the slow
			// tier services one packet only on a subset of steps and does
			// nothing on the others, so its bounded queue accumulates past
			// capacity and drops non-key overflow (keyframes still make room
			// via the pre-offer service above).
			if cfg.faults["slow"] && li == slowLayer {
				if pi%slowDrainEvery == 0 {
					serviceOnce(fan, li, &collected[li])
				}
			} else {
				drainAvailable(fan, li, &collected[li])
			}
		}
	}
	// Final drain of anything still buffered.
	for li := 0; li < nLayers; li++ {
		drainAvailable(fan, li, &collected[li])
	}

	// Isolate then independently recover the slow tier from the boundary.
	recoveredLayers := 0
	if cfg.faults["slow"] {
		fan.MarkDamaged(slowLayer)
		fan.Recover(slowLayer)
		recoveredLayers = 1
	}

	inputs := make([]manifest.LayerInput, nLayers)
	for li := 0; li < nLayers; li++ {
		stats := fan.Stats()[li]
		pkts := make([]manifest.ClockedPacket, 0, len(collected[li]))
		for _, p := range collected[li] {
			pkts = append(pkts, manifest.NewPacket(p.Seq, p.TS, p.Key, p.Bytes))
		}
		inputs[li] = manifest.LayerInput{
			Index: li, Name: cfgs[li].Name, TargetBPS: cfgs[li].TargetBPS,
			Packets: pkts, Dropped: stats.Dropped, Lost: stats.Lost,
			Recovered: stats.Recovered, LateKeys: lateKeys[li],
			Isolated: cfg.faults["slow"] && li == slowLayer,
		}
	}
	_ = durationTicks
	return inputs, recoveredLayers, nil
}

// serviceOnce dequeues a single packet if one is available.
func serviceOnce(fan *stream.Fanout, layer int, out *[]stream.Envelope) {
	if e, ok := fan.Queue(layer).TryDequeue(); ok {
		*out = append(*out, e)
	}
}

// drainAvailable empties whatever is currently queued without blocking.
func drainAvailable(fan *stream.Fanout, layer int, out *[]stream.Envelope) {
	for {
		e, ok := fan.Queue(layer).TryDequeue()
		if !ok {
			return
		}
		*out = append(*out, e)
	}
}

// buildTrace returns one deterministic layer trace. Layer 0 starts near the
// 32-bit modulus so its timestamps wrap during the run.
func buildTrace(layer int, clockRate, step uint32, n int) []*rtp.Packet {
	startTS := uint32(0)
	if layer == 0 {
		startTS = uint32(0xFFFFFFFF) - uint32(n)*step/2
	}
	spec := samplegen.RTPLayerSpec{
		SSRC: uint32(1000 + layer), PayloadType: 96, Packets: n,
		ClockRate: clockRate, StartTS: startTS, TStep: step,
		StartSeq: uint16(layer * 100),
	}
	return samplegen.BuildRTPTrace(spec)
}

func printSummary(cfg *demoConfig, m *manifest.Manifest, recoveredLayers int,
	isolated bool, parseErr, verifyErr error) {
	fmt.Printf("segments=%d\n", len(m.Segments))
	fmt.Printf("representations=%d\n", len(m.Representations))
	for _, r := range m.Representations {
		fmt.Printf("rep=%d name=%s bitrate_target_bps=%d bitrate_actual_bps=%d "+
			"bitrate_error_pct=%.2f quality=%.4f dropped_packets=%d missing_segments=%v\n",
			r.Index, r.Name, r.TargetBPS, r.ActualBPS, r.BitrateErrPct,
			r.Quality, r.Dropped, r.MissingSegs)
	}
	fmt.Printf("quality_mean=%.4f\n", manifest.QualityMean(m))
	fmt.Printf("alignment_error_ms=%d\n", m.AlignmentErrorMS)
	fmt.Printf("recovered_layers=%d\n", recoveredLayers)
	var totalDrop int64
	for _, r := range m.Representations {
		totalDrop += r.Dropped
	}
	fmt.Printf("dropped_packets=%d\n", totalDrop)
	fmt.Printf("summary=%s\n", m.Summary)
	switch {
	case isolated:
		fmt.Println("status=corruption-isolated")
	case verifyErr != nil:
		fmt.Println("status=invariant-failed")
	default:
		fmt.Println("status=ok")
	}
	if parseErr != nil {
		fmt.Fprintf(os.Stderr, "note: parse-stage recoverable errors: %v\n", parseErr)
	}
	if verifyErr != nil {
		fmt.Fprintf(os.Stderr, "note: verify: %v\n", verifyErr)
	}
}
