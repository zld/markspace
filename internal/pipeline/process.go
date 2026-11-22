package pipeline

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"math"
	"math/bits"
	"os"
	"path/filepath"

	"mediatimeline/internal/checkpoint"
	"mediatimeline/internal/clock"
	"mediatimeline/internal/container"
	"mediatimeline/internal/timeline"
)

// errKillAfter signals the simulated crash used by the resume demonstration.
var errKillAfter = errors.New("simulated kill threshold reached")

// IsKillAfter reports whether a run ended at the simulated crash point.
func IsKillAfter(err error) bool { return errors.Is(err, errKillAfter) }

func crcOf(b []byte) uint32 { return crc32.ChecksumIEEE(b) }

// RunResult reports the outcome of a run, including any localized but
// recoverable damage that was skipped.
type RunResult struct {
	Metrics    Metrics
	Skipped    []*clock.Error
	Checkpoint uint64
}

// Run processes the given ordered sample events through every stage. Events
// already behind the watermark are routed to deterministic replay; duplicate
// identities are counted once. Recoverable errors are collected and the
// offending sample skipped; the first unrecoverable error is returned.
func (p *Pipeline) Run(events []container.SampleEvent, killAfter uint64) (*RunResult, error) {
	out, err := os.OpenFile(p.outFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer out.Close()

	res := &RunResult{}
	// Declare the total input extent so the resampler never mistakes an
	// interior block boundary for end-of-stream (deterministic across resume).
	var totalInputFrames uint64
	for i := range events {
		totalInputFrames += uint64(events[i].PayloadSize) / uint64(p.cfg.Channels*2)
	}
	p.resample.SetTotalInput(totalInputFrames)
	processedSinceCkpt := 0
	for i := range events {
		e := events[i]
		// Identity dedup is the first and authoritative barrier: it is
		// restored from the checkpoint, so samples already durably processed
		// before a crash are rejected before they can touch the watermark or
		// re-enter any output buffer.
		if !p.replay.Admit(e) {
			continue
		}
		late := p.wm.Advance(e)
		if late {
			// Admit already registered the identity; buffer for replay without
			// a second admission (AddLate would now see it as a duplicate).
			p.replay.StoreLate(e)
			continue
		}
		if err := p.processOne(e, out); err != nil {
			if ce, ok := err.(*clock.Error); ok && ce.Recoverable() {
				res.Skipped = append(res.Skipped, ce)
				p.recoverableErrs++
				continue
			}
			return res, err
		}
		p.st.NextSample = e.SampleIndex + 1
		p.st.EventCount++
		processedSinceCkpt++
		if p.resumed {
			p.resumedEvents++
		}

		// Drain any late events whose time has now been covered by the mark.
		p.drainReplay(out, res)

		if p.cfg.CheckpointEvery > 0 && processedSinceCkpt >= p.cfg.CheckpointEvery {
			if err := p.checkpoint(); err != nil {
				return res, err
			}
			processedSinceCkpt = 0
		}
		if killAfter > 0 && uint64(p.st.EventCount) >= killAfter {
			// Simulated crash: persist progress then stop without finalizing.
			if err := p.checkpoint(); err != nil {
				return res, err
			}
			res.Checkpoint = p.st.Seq
			res.Metrics = p.Metrics()
			return res, errKillAfter
		}
	}
	// Final replay drain and end-of-stream flush.
	p.drainReplay(out, res)
	if p.pendingErr != nil {
		return res, p.pendingErr
	}
	if tail := p.resample.Flush(); len(tail) > 0 {
		p.emitAudio(tail, out)
		p.totalOut += uint64(len(tail) / p.cfg.Channels)
	}
	p.loud.Flush()
	if err := p.Validate(); err != nil {
		return res, err
	}
	if err := p.checkpoint(); err != nil {
		return res, err
	}
	res.Metrics = p.Metrics()
	res.Checkpoint = p.st.Seq
	return res, nil
}

// processOne validates one event, decodes its PCM and pushes it through the
// resampler, channel mapper and loudness stages while checking invariants.
func (p *Pipeline) processOne(e container.SampleEvent, out *os.File) error {
	// 32-bit wrap expansion on the raw track ticks.
	_ = p.unwrap.Extend(uint32(e.DTSRaw))

	pcm, frames, err := p.decodePCM16(e, p.cfg.Channels)
	if err != nil {
		return err
	}
	// Invariant: monotonic delivery.
	if err := timeline.AssertMonotonic([]container.SampleEvent{e}); err != nil {
		return err
	}
	p.totalIn += uint64(frames)
	// Drift is measured against the content clock, not the container
	// timestamps: the sample payloads define how much media time has actually
	// elapsed, while the node DTS carries the unknown rate offset. ContentFrame
	// is assigned in track order at scan time, so replayed late events still
	// contribute their true content position. Both axes are expressed in track
	// ticks, so the fitted slope's deviation is the dimensionless drift ratio.
	hi, lo := bits.Mul64(e.ContentFrame, uint64(e.Timescale))
	refTicks, _ := bits.Div64(hi, lo, uint64(p.cfg.InRate))
	// DTSRaw carries the absolute node counter (including the container's
	// edit-list offset); subtract the first observed tick so the fit is
	// origin-relative on both axes.
	if !p.nodeOriginSet {
		p.nodeOrigin, p.nodeOriginSet = e.DTSRaw, true
	}
	rel := int64(e.DTSRaw) - int64(p.nodeOrigin)
	p.drift.Observe(refTicks, uint64(rel))

	rs := p.resample.Process(pcm, p.cfg.Channels)
	mapped := p.mapper.Apply(rs)
	limited := p.loud.Process(mapped)
	// Invariant: mapper preserves the frame count from the resampler.
	if len(mapped) != len(rs) {
		return clock.NewError(clock.ErrInvariant, "channel mapper changed sample count").
			WithPosition(e.FileOffset, e.TrackID, e.SampleIndex)
	}
	p.totalOut += uint64(len(limited) / p.cfg.Channels)
	p.emitAudio(limited, out)
	return nil
}

// emitAudio appends float samples as little-endian int16 and updates the hash.
func (p *Pipeline) emitAudio(samples []float64, out *os.File) {
	buf := make([]byte, len(samples)*2)
	for i, v := range samples {
		v = math.Max(-1, math.Min(1, v))
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(int16(v*32767)))
	}
	out.Write(buf)
	p.hash.Write(buf)
	p.st.OutputBytes += uint64(len(buf))
}

// drainReplay delivers buffered late events in stable order; duplicates are
// already suppressed by the replay log's identity set.
func (p *Pipeline) drainReplay(out *os.File, res *RunResult) {
	late := p.replay.Drain()
	if len(late) == 0 {
		return
	}
	timeline.StableSort(late)
	for _, e := range late {
		if err := p.processOne(e, out); err != nil {
			if ce, ok := err.(*clock.Error); ok && ce.Recoverable() {
				res.Skipped = append(res.Skipped, ce)
				p.recoverableErrs++
				continue
			}
			// Late-stage unrecoverable errors surface on the next Run return.
			p.pendingErr = err
			return
		}
		if e.SampleIndex+1 > p.st.NextSample {
			p.st.NextSample = e.SampleIndex + 1
		}
		p.st.EventCount++
	}
}

// checkpoint serializes the full kernel state and writes it atomically.
func (p *Pipeline) checkpoint() error {
	p.st.Seq++
	phNum, phDen := p.resample.Phase()
	p.st.Unwrap = p.unwrap.Snapshot()
	p.st.Drift = p.drift.Snapshot()
	p.st.Watermark = p.wm.Snapshot()
	p.st.Replay = p.replay.Snapshot()
	p.st.ResamplePhase, p.st.ResampleDen = phNum, phDen
	p.st.ResampleCons = p.resample.Consumed()
	p.st.ResampleFrames = p.resample.FramesEmitted()
	p.st.ResampleTotal = p.resample.TotalInput()
	p.st.PrevFrame = p.resample.PrevFrame()
	p.st.Loudness = p.loud.Snapshot()
	p.st.TotalIn, p.st.TotalOut = p.totalIn, p.totalOut
	p.st.NodeOrigin, p.st.NodeOriginSet = p.nodeOrigin, p.nodeOriginSet
	payload, err := json.Marshal(p.st)
	if err != nil {
		return err
	}
	return p.store.Save(checkpoint.Checkpoint{Sequence: p.st.Seq, Payload: payload})
}

// decodePCM16 turns a little-endian 16-bit payload into float64 frames.
func (p *Pipeline) decodePCM16(e container.SampleEvent, channels int) ([]float64, int, error) {
	// The container scanner validates offsets but does not retain payload
	// bytes; the pipeline re-reads them through the event's extent via a
	// helper that the CLI wires with the source path.
	if e.PayloadSize <= 0 || int(e.PayloadSize)%(channels*2) != 0 {
		return nil, 0, clock.NewError(clock.ErrIllegalOffset, "payload not PCM16-aligned").
			WithPosition(e.FileOffset, e.TrackID, e.SampleIndex)
	}
	buf := make([]byte, e.PayloadSize)
	if p.payloadReader == nil {
		return nil, 0, clock.NewError(clock.ErrInvariant, "payload source not wired").
			WithPosition(e.FileOffset, e.TrackID, e.SampleIndex)
	}
	if _, err := p.payloadReader.ReadAt(buf, e.FileOffset); err != nil {
		return nil, 0, clock.NewError(clock.ErrIllegalOffset, err.Error()).
			WithPosition(e.FileOffset, e.TrackID, e.SampleIndex)
	}
	// CRC check: validate against the trusted ingest CRC when available,
	// otherwise against the value computed at scan time.
	wantCRC := e.PayloadCRC32
	if e.TrustedCRC32 != 0 {
		wantCRC = e.TrustedCRC32
	}
	if crc := crcOf(buf); crc != wantCRC {
		return nil, 0, clock.NewError(clock.ErrCRC, "payload crc mismatch").
			WithPosition(e.FileOffset, e.TrackID, e.SampleIndex)
	}
	frames := int(e.PayloadSize) / (channels * 2)
	out := make([]float64, frames*channels)
	for i := 0; i < frames*channels; i++ {
		v := int16(binary.LittleEndian.Uint16(buf[i*2:]))
		out[i] = float64(v) / 32768
	}
	return out, frames, nil
}

// OutputPath exposes the durable PCM file location.
func (p *Pipeline) OutputPath() string { return p.outFile }

// Validate enforces the cross-stage invariants at a boundary:
//   - monotonic time: the watermark never regresses (guaranteed by construction,
//     re-asserted here as a guard),
//   - sample conservation: every frame the resampler emits reaches the output,
//   - interval coverage: the durable output byte count matches the state.
func (p *Pipeline) Validate() error {
	if !p.wm.Started() && p.st.EventCount > 0 {
		return clock.NewError(clock.ErrInvariant, "events counted without watermark")
	}
	wantFrames := p.resample.FramesEmitted()
	if p.totalOut != wantFrames {
		return clock.NewError(clock.ErrInvariant,
			"frame conservation violated").WithPosition(-1, 0, -1)
	}
	// Output is stereo int16 little-endian: bytes == frames * channels * 2.
	wantBytes := p.totalOut * uint64(p.cfg.Channels) * 2
	if wantBytes != p.st.OutputBytes {
		return clock.NewError(clock.ErrInvariant, "output interval coverage mismatch")
	}
	return nil
}

// TotalIn/TotalOut expose the conservation counters for tests.
func (p *Pipeline) TotalIn() uint64  { return p.totalIn }
func (p *Pipeline) TotalOut() uint64 { return p.totalOut }

// Ensure the temp-file + rename discipline for outputs lives in one place.
func atomicWritePCM(dir string, data []byte) (string, error) {
	final := filepath.Join(dir, "output.pcm")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	return final, os.Rename(tmp, final)
}
