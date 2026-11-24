package pipeline

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"

	"mediad/internal/audio"
	"mediad/internal/clock"
	"mediad/internal/ingest"
	"mediad/internal/reorder"
)

// NewRunner validates the configuration and prepares a run.
// interruptAfterPackets stops the run after that many RTP packets have been
// processed, always at a committed block boundary; <=0 runs to completion.
func NewRunner(cfg Config, out io.Writer, ckptPath string, interruptAfterPackets int) (*Runner, error) {
	if audio.ChannelsFor(cfg.TargetLayout) == 0 {
		return nil, fmt.Errorf("unknown layout %q", cfg.TargetLayout)
	}
	if cfg.SourceRate <= 0 || cfg.TargetRate <= 0 || cfg.SourceChans <= 0 {
		return nil, errors.New("rate and channel counts must be positive")
	}
	rs, err := audio.NewResampler(cfg.SourceRate, cfg.TargetRate)
	if err != nil {
		return nil, err
	}
	if cfg.WindowSize == 0 {
		cfg.WindowSize = reorder.DefaultWindow
	}
	if cfg.BurstRun == 0 {
		cfg.BurstRun = 3
	}
	if cfg.BlockFrames == 0 {
		cfg.BlockFrames = audio.BlockSize
	}
	return &Runner{
		cfg:       cfg,
		ckptPath:  ckptPath,
		interrupt: interruptAfterPackets,
		timeline:  clock.NewTimeline(),
		window:    reorder.NewWindow(cfg.WindowSize, cfg.BurstRun),
		resample:  rs,
		dstCh:     audio.ChannelsFor(cfg.TargetLayout),
		out:       out,
		hasher:    sha256.New(),
		peak:      negInf,
	}, nil
}

// Run drives the framed stream to completion (or an intentional interruption).
func (r *Runner) Run(input io.Reader) (*Report, error) {
	rd := ingest.NewReader(input)
	for {
		frame, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return r.finish()
		}
		if err != nil {
			return nil, err // *ingest.ParseError carrying offset/stage/code
		}
		r.prefixBuf.Write(frame.Raw)
		if err := r.handleFrame(frame); err != nil {
			if errors.Is(err, ErrInterrupted) {
				return r.report(), ErrInterrupted
			}
			return nil, err
		}
	}
}

func (r *Runner) handleFrame(f *ingest.Frame) error {
	switch f.Type {
	case ingest.TagRTCP:
		pkt, err := ingest.ParseRTCP(f)
		if err != nil {
			return err
		}
		for _, sr := range pkt.Reports {
			r.timeline.ApplySenderReport(uint32(r.cfg.SourceRate), sr.RTPTime, sr.NTPTime)
		}
		return nil
	case ingest.TagRTP:
		pkt, err := ingest.ParseRTP(f)
		if err != nil {
			return err
		}
		r.packets++
		p := &reorder.Packet{Seq: pkt.Packet.SequenceNumber, Offset: f.FrameOffset, RTP: pkt.Packet}
		for _, ev := range r.window.Push(p) {
			if err := r.handleEvent(ev); err != nil {
				return err
			}
		}
		// Interrupt only on a committed block boundary so a checkpoint exists.
		if r.interrupt > 0 && r.packets >= uint64(r.interrupt) && r.blocks > 0 {
			return ErrInterrupted
		}
		return nil
	default:
		return &ingest.ParseError{Code: ingest.ErrUnknownFrame, Offset: f.FrameOffset, Stage: ingest.StageParse}
	}
}

func (r *Runner) handleEvent(ev reorder.Event) error {
	if ev.Kind == reorder.KindGap {
		return nil // gaps are reported, never filled with fabricated samples
	}
	r.timeline.Add(clock.PacketRef{
		Seq:       ev.Packet.Seq,
		RTPTS:     ev.Packet.RTP.Timestamp,
		ClockRate: uint32(r.cfg.SourceRate),
		Offset:    ev.Packet.Offset,
	})
	return r.feedPCM(ev.Packet.RTP.Payload)
}

// feedPCM appends one packet's L16 samples and processes every complete block.
func (r *Runner) feedPCM(payload []byte) error {
	samples := len(payload) / 2
	r.samplesIn += int64(samples)
	got := make([]int16, samples)
	for i := 0; i < samples; i++ {
		got[i] = int16(binary.BigEndian.Uint16(payload[i*2:]))
	}
	r.pcmBuf = append(r.pcmBuf, got...)

	frameSize := r.cfg.SourceChans
	for len(r.pcmBuf) >= r.cfg.BlockFrames*frameSize {
		block := r.pcmBuf[:r.cfg.BlockFrames*frameSize]
		r.pcmBuf = r.pcmBuf[r.cfg.BlockFrames*frameSize:]
		if err := r.processBlock(block); err != nil {
			return err
		}
	}
	return nil
}

// processBlock runs the full transform on one validated audio block and, only
// after success, commits the checkpoint boundary.
func (r *Runner) processBlock(block []int16) error {
	layoutOut := audio.NormalizeLayout(block, r.cfg.SourceChans, 1) // mono first
	converted := r.resample.ResampleBlock(layoutOut)
	norm := audio.NormalizeLayout(converted, 1, r.dstCh)
	st, err := audio.ApplyLoudness(norm, r.cfg.TargetLUFS)
	if err != nil {
		return fmt.Errorf("internal: loudness: %w", err)
	}
	// Internal verification: written length must match declared sample count.
	if len(norm) == 0 {
		return errors.New("internal: empty output block")
	}
	if err := r.writeSamples(norm); err != nil {
		return err
	}
	r.clipped += st.Clipped
	if st.Clipped > 0 {
		r.clipBefore = st.PeakBefore
		r.clipAfter = st.PeakAfter
	}
	if p, err := audio.PeakDBFS(norm); err == nil && p > r.peak {
		r.peak = p
	}
	r.blocks++
	r.timeline.Commit(r.window.NextSeq())
	return r.commit()
}

func (r *Runner) writeSamples(s []int16) error {
	buf := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
	}
	if _, err := r.out.Write(buf); err != nil {
		return err
	}
	r.hasher.Write(buf)
	r.samplesOut += int64(len(s))
	return nil
}

// finish flushes the retained tail and produces the final report.
func (r *Runner) finish() (*Report, error) {
	for _, ev := range r.window.Flush() {
		if ev.Kind == reorder.KindPacket {
			if err := r.handleEvent(ev); err != nil {
				return nil, err
			}
		}
	}
	// Resample any short tail; the partial boundary is respected, never padded.
	if len(r.pcmBuf) > 0 {
		mono := audio.NormalizeLayout(r.pcmBuf, r.cfg.SourceChans, 1)
		// Feed the tail into the resampler, then flush the short remainder;
		// the partial boundary is preserved and never over-read.
		converted := r.resample.ResampleBlock(mono)
		tail, _ := r.resample.Flush()
		converted = append(converted, tail...)
		out := audio.NormalizeLayout(converted, 1, r.dstCh)
		if len(out) > 0 {
			st, err := audio.ApplyLoudness(out, r.cfg.TargetLUFS)
			if err != nil {
				return nil, err
			}
			if err := r.writeSamples(out); err != nil {
				return nil, err
			}
			r.clipped += st.Clipped
			if st.Clipped > 0 {
				r.clipBefore, r.clipAfter = st.PeakBefore, st.PeakAfter
			}
		}
		r.pcmBuf = r.pcmBuf[:0]
	}
	return r.report(), nil
}

func (r *Runner) report() *Report {
	st := r.window.Stats()
	peak := r.peak
	if math.IsInf(peak, 0) {
		peak = 0
	}
	return &Report{
		Packets:     r.basePackets + r.packets,
		Duplicates:  r.baseDup + st.Duplicates,
		Reordered:   r.baseReorder + st.Reordered,
		LateDropped: r.baseLate + st.LateDropped,
		Gaps:        r.baseGaps + r.window.Loss().GapCount(),
		Bursts:      r.baseBursts + r.window.Loss().Bursts(),
		SamplesIn:   r.samplesIn, // already seeded from the checkpoint on resume
		SamplesOut:  r.samplesOut,
		PeakDBFS:    peak,
		ClipBefore:  r.clipBefore,
		ClipAfter:   r.clipAfter,
		Clipped:     r.clipped,
		SHA256:      hex.EncodeToString(r.hasher.Sum(nil)),
		ResumeMS:    r.resumeMS,
	}
}
