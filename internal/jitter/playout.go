package jitter

import (
	"time"
)

// AdvanceTo runs playout up to now, emitting due real or concealed frames and
// recording every scheduled watermark sample.
func (c *Controller) AdvanceTo(now time.Duration) {
	if now < c.now {
		now = c.now
	}
	if c.sampling {
		for c.nextSample <= now {
			c.pumpTo(c.nextSample)
			c.sampleAt(c.nextSample)
			c.nextSample += c.sampleEach
		}
	}
	c.pumpTo(now)
	c.now = now
}

func (c *Controller) pumpTo(now time.Duration) {
	for _, id := range c.ids {
		c.pumpStream(c.streams[id], now)
	}
}

func (c *Controller) pumpStream(st *playState, now time.Duration) {
	if st == nil || !st.started || st.ended || st.suspended {
		return
	}
	for i := 0; i < 1_000_000; i++ {
		if st.playAt > now {
			st.catchingUp = false
			return
		}
		if pkt, ok := c.q.Pop(st.id); ok {
			st.waiting = false
			c.playReal(st, pkt, now)
			continue
		}
		if c.inputClosed && c.q.Buffered(st.id) == 0 {
			// The source is finished and the stream has drained. Stop the
			// clock rather than concealing an endless silent tail.
			st.ended = true
			return
		}
		if c.q.Buffered(st.id) == 0 &&
			now-st.lastArrival > time.Duration(c.cfg.InterruptMS)*time.Millisecond {
			// Nothing is buffered and non-arrival has exceeded the
			// interruption threshold: the sender is gone. Suspend instead of
			// concealing forever, so a finished stream does not report a
			// huge fabricated hidden run. A later packed resumes the clock.
			st.suspended = true
			c.interruptions++
			return
		}
		if !st.catchingUp {
			if !st.waiting {
				st.waiting = true
				st.waitUntil = st.playAt + c.waitDuration(st)
			}
			if now < st.waitUntil {
				return
			}
			st.waiting = false
			// Once a frame has exceeded its configured wait, conceal it and
			// all other overdue frames immediately: the play clock then
			// re-synchronises with wall time instead of drifting.
			st.catchingUp = true
		}
		c.playHidden(st)
	}
}

func (c *Controller) playReal(st *playState, pkt Store, now time.Duration) {
	frames := int(pkt.Frames)
	if frames < 1 {
		frames = 1
	}
	capture := captureToLocal(st, pkt.Timestamp)
	// Latency is the wall-clock moment the frame is emitted minus its
	// estimated capture instant. Overdue frames (emitted after a wait) show up
	// in the upper percentiles.
	latency := durToMS(now - capture)
	if latency < 0 {
		latency = 0
	}
	c.m.AddLatency(latency)
	for i := 0; i < frames; i++ {
		st.conceal.RealFrame()
	}
	st.playAt = c.nextDeadline(st, capture, frames)
}

// nextDeadline schedules the frame after one just played. The baseline is the
// follow-on frame interval, then a bounded slew corrects the drift between the
// current playout instant and the deadline the current watermark implies.
// Because the correction is at most 2 ms per frame (10 % at 20 ms), the
// watermark moves smoothly in both directions and a watermark change never
// causes an abrupt jump or a burst of concealment.
func (c *Controller) nextDeadline(st *playState, capture time.Duration, frames int) time.Duration {
	frame := time.Duration(c.cfg.FrameMS) * time.Millisecond
	next := st.playAt + time.Duration(frames)*frame
	desired := capture + time.Duration(frames)*frame + msToDur(st.targetMS)
	drift := durToMS(next - desired)
	const maxSlewMS = 2.0
	if drift > maxSlewMS {
		drift = maxSlewMS
	} else if drift < -maxSlewMS {
		drift = -maxSlewMS
	}
	return next - msToDur(drift)
}

func (c *Controller) playHidden(st *playState) {
	seq, _ := c.q.NextSeq(st.id)
	st.conceal.HiddenFrame(st.id, seq)
	c.q.Advance(st.id)
	c.q.Cleanup(st.id)
	// Concealed slots advance the cursor and the clock together. The cursor
	// is never skipped: a lag is recovered by draining buffered packets
	// back-to-back, or, where frames are genuinely missing, by a contiguous
	// hidden run. Jumping the clock ahead here would decouple playAt from the
	// cursor and make the buffer overflow permanently.
	st.playAt += time.Duration(c.cfg.FrameMS) * time.Millisecond
}

// DrainTail stops accepting new packets and advances the playout clock in 5 ms
// steps until every stream has drained or maxWait elapses. It is used by the
// live UDP path so the buffered tail is still emitted when a session ends.
func (c *Controller) DrainTail(maxWait time.Duration) time.Duration {
	c.CloseInput()
	step := 5 * time.Millisecond
	deadline := c.now + maxWait
	for c.now < deadline && c.HasWork() {
		c.AdvanceTo(c.now + step)
	}
	return c.now
}

func (c *Controller) sampleAt(t time.Duration) {
	target := 0.0
	for _, id := range c.ids {
		if st := c.streams[id]; st != nil && st.started && !st.ended && st.targetMS > target {
			target = st.targetMS
		}
	}
	c.m.AddSample(t.Milliseconds(), c.q.BufferedFrames(), float64(c.cfg.FrameMS), target, c.q.StreamCount())
}
