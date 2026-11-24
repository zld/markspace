package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"

	"medkernel/audio"
	"medkernel/media"
	"medkernel/rtp"
	"medkernel/ts"
)

func (r *Runner) stageParse(workdir string, fx *Fixtures) (string, map[string]float64, error) {
	// Parse over an independent reader so the source buffer stays intact and
	// a later recovery run over the same fixture sees identical input bytes.
	// Reset derived state so re-parsing an already-seen fixture is idempotent.
	fx.Packets = nil
	p := ts.NewParser(ts.Size188)
	if err := p.Parse(bytes.NewReader(fx.TS.Bytes()), func(pk ts.Packet) error {
		cp := pk
		cp.Raw = append([]byte(nil), pk.Raw...)
		cp.Payload = append([]byte(nil), pk.Payload...)
		cp.Adaptation = append([]byte(nil), pk.Adaptation...)
		fx.Packets = append(fx.Packets, cp)
		return nil
	}); err != nil {
		return "", nil, err
	}
	fx.TSFaults = p.Faults
	metrics := map[string]float64{
		"packets":     float64(p.Stats.Total),
		"sync_errors": float64(p.Stats.SyncErrors),
		"truncated":   float64(p.Stats.Truncated),
	}
	return hashBytes(p.Faults), metrics, nil
}

func (r *Runner) stageClock(workdir string, fx *Fixtures, m *Metrics) (string, map[string]float64, error) {
	pcr := ts.NewPCRTracker(3600, 100, 60_000)
	psi := ts.NewPSIWatcher()
	checker := ts.NewContinuityChecker()
	var clockFaults int64
	for _, pk := range fx.Packets {
		if err := psi.Feed(pk); err != nil {
			return "", nil, err
		}
		if ev, bad := checker.Check(pk); bad && !ev.Duplicate {
			m.FaultRanges = append(m.FaultRanges, FaultRange{
				Class: string(media.ClassContinuity), PID: ev.PID,
				Index: ev.Index, ByteOff: ev.ByteOff, ByteEnd: ev.ByteEnd,
			})
		}
		if pk.PCR27 >= 0 {
			if _, err := pcr.Push(pk.PCR27, pk.PID, pk.Index); err != nil {
				clockFaults++
				m.FaultRanges = append(m.FaultRanges, FaultRange{
					Class: string(media.ClassClock), PID: pk.PID, Index: pk.Index,
					ByteOff: pk.Offset, ByteEnd: pk.Offset + int64(pk.Size),
				})
			}
		}
	}
	fx.PSIBounds = psi.Boundaries
	for _, f := range fx.TSFaults {
		m.FaultRanges = append(m.FaultRanges, FaultRange{
			Class: string(f.Class), PID: f.PID, Index: f.Index,
			ByteOff: f.ByteOff, ByteEnd: f.ByteEnd,
		})
	}
	return hashBytes(psi.Boundaries), map[string]float64{
		"clock_faults":  float64(clockFaults),
		"psi_boundary":  float64(len(psi.Boundaries)),
		"cc_duplicates": float64(checker.Stats.Duplicates),
		"cc_gaps":       float64(checker.Stats.Gaps),
	}, nil
}

func (r *Runner) stageAudio(workdir string, fx *Fixtures, m *Metrics) (string, map[string]float64, error) {
	src := audio.Frame{Samples: synthMono(4096, 0.5), Channels: 1, Rate: 48000}
	rs, err := audio.NewResampler(48000, int(fx.PMT.SampleRate))
	if err != nil {
		return "", nil, err
	}
	rateOut, err := rs.Process(src)
	if err != nil {
		return "", nil, err
	}
	mapper, err := audio.NewLayoutMapper(1, int(fx.PMT.Channels), nil)
	if err != nil {
		return "", nil, err
	}
	layoutOut, err := mapper.Process(rateOut)
	if err != nil {
		return "", nil, err
	}
	norm := audio.NewNormalizer(r.thresholds.LUFSTarget, 0.95)
	norm.Measure(layoutOut)
	final := norm.Normalize(layoutOut)
	st := norm.Stats()
	m.AudioLUFS = st.IntegratedLUFS
	return hashBytes(final.Samples), map[string]float64{
		"audio_lufs":     st.IntegratedLUFS,
		"audio_clips":    float64(st.Clips),
		"audio_peak":     float64(st.TruePeak),
		"loudness_delta": st.LoudnessDelta,
	}, nil
}

func (r *Runner) stageRTP(workdir string, fx *Fixtures, m *Metrics) (string, map[string]float64, error) {
	jb := fx.NewJitter()
	var released []rtp.Packet
	for _, p := range fx.RTPPackets {
		_ = jb.Push(rtp.FromPion(p))
		released = append(released, jb.Flush()...)
	}
	released = append(released, jb.Drain()...)
	st := jb.Stats()
	m.RecoveredPackets, m.LossPackets = st.Recovered, st.Loss
	m.RecoveryRatePct = jb.RecoveryRatePercent()
	// Packet slots map to 20 ms frames at 50 packets/s; p95 in slots -> ms.
	m.P95LatencyMS = float64(st.P95Latency) * 20
	return hashPackets(released), map[string]float64{
		"recovered_packets": float64(st.Recovered),
		"loss_packets":      float64(st.Loss),
		"p95_latency_ms":    m.P95LatencyMS,
		"recovery_rate":     m.RecoveryRatePct,
	}, nil
}

func (r *Runner) stageSync(workdir string, fx *Fixtures, m *Metrics) (string, map[string]float64, error) {
	// Compare audio and video PTS progression on the shared 90 kHz axis.
	// Fixtures are generated with aligned per-second ticks, so the residual
	// is bounded by the packetization granularity.
	// Each elementary stream owns its PTS unwrapper; packets of different
	// tracks may legitimately share a PTS (and interleave on the wire).
	trackers := map[uint16]*ts.PTSTracker{}
	// A/V sync compares the two tracks at the same frame index. Pairing by
	// frame rather than wire order removes the one-frame interleave offset.
	audioPTS := map[uint64]int64{}
	videoPTS := map[uint64]int64{}
	var maxDelta float64
	for _, pk := range fx.Packets {
		if !pk.HasPTS {
			continue
		}
		tr := trackers[pk.PID]
		if tr == nil {
			tr = ts.NewPTSTracker(100)
			trackers[pk.PID] = tr
		}
		ext, err := tr.Push(pk.PTS90, pk.PID, pk.Index)
		if err != nil {
			return "", nil, err
		}
		frame := pk.PTS90 / ptsStep90
		switch pk.PID {
		case fx.AudioPID:
			audioPTS[frame] = ext
		case fx.VideoPID:
			videoPTS[frame] = ext
		default:
			continue
		}
		if a, okA := audioPTS[frame]; okA {
			if v, okV := videoPTS[frame]; okV {
				d := a - v
				if d < 0 {
					d = -d
				}
				if ms := media.TicksToMillis(float64(d)); ms > maxDelta {
					maxDelta = ms
				}
			}
		}
	}
	m.SyncErrorMS = maxDelta
	return hashBytes(fx.PSIBounds), map[string]float64{"sync_error_ms": maxDelta}, nil
}

// OutputPath joins a path inside the run directory.
func (r *Runner) OutputPath(name string) string { return filepath.Join(r.dir, name) }

func hashBuffer(fx *Fixtures) string {
	h := sha256.New()
	h.Write(fx.TS.Bytes())
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func hashRTP(fx *Fixtures) string {
	h := sha256.New()
	for _, p := range fx.RTPPackets {
		h.Write(p.Payload)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func hashPackets(pkts []rtp.Packet) string {
	h := sha256.New()
	for _, p := range pkts {
		h.Write(p.Payload)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
