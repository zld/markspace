package jitter

import (
	"math"
	"time"

	"jitterlab/internal/packet"
	"jitterlab/internal/plc"
)

// AddPacket validates and buffers one wire packet arriving at arrival. It
// returns true only when the packet was accepted into the jitter buffer.
func (c *Controller) AddPacket(data []byte, arrival time.Duration) bool {
	c.received++
	p, err := packet.Unmarshal(data)
	if err != nil {
		c.invalid++
		return false
	}
	if arrival > c.now {
		c.now = arrival
	}
	if !c.q.Push(p, arrival) {
		return false
	}
	st, ok := c.streams[p.Header.StreamID]
	if !ok {
		st = &playState{
			id:       p.Header.StreamID,
			targetMS: c.cfg.InitialTargetMS,
			conceal:  plc.New(c.cfg.HiddenThreshold),
		}
		c.streams[p.Header.StreamID] = st
		c.ids = append(c.ids, p.Header.StreamID)
		if !c.sampling {
			// Start the watermark clock with the first stream, so a run that
			// receives nothing reports zero samples rather than a long zero
			// curve.
			c.sampling = true
			c.nextSample = (arrival / c.sampleEach) * c.sampleEach
		}
	}
	if !st.started {
		st.started = true
		st.baseTS = p.Header.Timestamp
		st.firstArrival = arrival
		st.playAt = arrival + msToDur(st.targetMS)
		st.lastArrival = arrival
		st.haveArrival = true
		return true
	}
	if st.suspended {
		c.resume(st, arrival)
	}
	c.observe(st, arrival, p.Header.Timestamp, p.Header.Seq)
	return true
}

// resume restarts the playout clock after a suspension. The cursor jumps to
// the oldest buffered packet; the slots skipped over were never received, so
// they are counted as stream interruptions rather than concealed frames.
func (c *Controller) resume(st *playState, arrival time.Duration) {
	c.q.Resync(st.id)
	st.suspended = false
	st.catchingUp = false
	st.waiting = false
	st.conceal.Reset()
	st.playAt = arrival + msToDur(st.targetMS)
	// Re-anchor the capture mapping on the first packet buffered after the
	// gap, otherwise latency measured across the silence would be enormous.
	if pkt, ok := c.q.Peek(st.id); ok {
		st.baseTS = pkt.Timestamp
		st.firstArrival = arrival
		st.haveTransit = false
	}
}

func (c *Controller) observe(st *playState, arrival time.Duration, ts uint32, seq uint32) {
	if st.haveArrival {
		gap := arrival - st.lastArrival
		if gap > time.Duration(c.cfg.InterruptMS)*time.Millisecond {
			c.interruptions++
		}
	}
	st.lastArrival = arrival
	st.haveArrival = true

	// The offset between the capture and arrival clocks is unknown. The anchor
	// starts at the first packet's arrival and slides backwards whenever a
	// less-delayed packet appears. Transit is then the excess delay beyond the
	// fastest delivery observed so far, which is exactly the delay a buffer
	// must absorb to avoid concealment.
	local := captureToLocal(st, ts)
	if offset := arrival - local; offset < 0 {
		st.firstArrival += offset
		local = arrival
	}
	transit := arrival - local
	if st.haveTransit {
		d := math.Abs(durToMS(transit - st.lastTransit))
		if d > 1000 {
			d = 1000
		}
		st.jitterMS += (d - st.jitterMS) / 16
		desired := c.cfg.MinTargetMS + 4*st.jitterMS
		st.adapt(desired, c.cfg)
	}
	st.haveTransit = true
	st.lastTransit = transit
}

// captureToLocal maps a capture timestamp onto the receiver clock using the
// stream's first packet as the origin. Unsigned subtraction makes sequence
// wraparound safe across 2^32 milliseconds.
func captureToLocal(st *playState, ts uint32) time.Duration {
	return st.firstArrival + time.Duration(int64(ts-st.baseTS))*time.Millisecond
}

// playDeadline is the wall-clock instant at which a packet with capture time
// ts is due, given the stream's target watermark.
func playDeadline(st *playState, ts uint32) time.Duration {
	return captureToLocal(st, ts) + msToDur(st.targetMS)
}

// adapt moves the target watermark toward the estimate. Increases are capped
// per packet so a single wild arrival cannot cause a large jump; decreases are
// slower still, letting the watermark settle during stable periods.
func (st *playState) adapt(desired float64, cfg Config) {
	if desired < cfg.MinTargetMS {
		desired = cfg.MinTargetMS
	}
	if desired > cfg.MaxTargetMS {
		desired = cfg.MaxTargetMS
	}
	step := 1.5
	if desired < st.targetMS {
		step = 0.25
	}
	diff := desired - st.targetMS
	switch {
	case math.Abs(diff) <= step:
		st.targetMS = desired
	case diff > 0:
		st.targetMS += step
	default:
		st.targetMS -= step
	}
	if st.targetMS < cfg.MinTargetMS {
		st.targetMS = cfg.MinTargetMS
	}
	if st.targetMS > cfg.MaxTargetMS {
		st.targetMS = cfg.MaxTargetMS
	}
}

func (c *Controller) waitDuration(st *playState) time.Duration {
	ms := st.targetMS
	if ms < float64(c.cfg.FrameMS) {
		ms = float64(c.cfg.FrameMS)
	}
	if ms > float64(c.cfg.WaitMS) {
		ms = float64(c.cfg.WaitMS)
	}
	return msToDur(ms)
}

// Finalize copies queue and concealment counters into the summary and computes
// percentiles, continuity and the buffer sample count.
func (c *Controller) Finalize() {
	qs := c.q.Stats()
	c.m.PacketsReceived = c.received
	c.m.PacketsInvalid = c.invalid
	c.m.PacketsAccepted = qs.Accepted
	c.m.DropDuplicate = qs.Duplicate
	c.m.DropLate = qs.Late
	c.m.DropOverflow = qs.Overflow
	c.m.DropAmbiguous = qs.Ambiguous
	c.m.DropTimestampRegression = qs.Regression
	c.m.StreamLimitDrops = qs.StreamDrop
	c.m.StreamInterruptions = c.interruptions

	var real, hidden, maxRun, degradations int64
	for _, id := range c.ids {
		st := c.streams[id]
		if st == nil || st.conceal == nil {
			continue
		}
		s := st.conceal.Stats()
		real += s.RealFrames
		hidden += s.HiddenFrames
		if s.MaxRun > maxRun {
			maxRun = s.MaxRun
		}
		degradations += s.Degradations
	}
	c.m.FramesPlayed = real
	c.m.HiddenFrames = hidden
	c.m.MaxHiddenRun = maxRun
	c.m.HiddenDegrades = degradations
	c.m.Finalize()
}
