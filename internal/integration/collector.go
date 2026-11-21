package integration

import (
	"sync"
	"time"

	"github.com/pion/webrtc/v4"

	"looprelay/internal/metrics"
	rtpsource "looprelay/internal/rtp"
)

// collector reads the egress track and keeps sequence, timestamp and jitter
// statistics plus a planned-loss model so intentional skips are not reported
// as network loss.
type collector struct {
	mu       sync.Mutex
	seq      *metrics.SequenceAnalyzer
	ts       *metrics.TimestampAnalyzer
	jitter   *metrics.Jitter
	skip     []bool
	startSeq uint16
	expected map[int64]bool
	received int
	readErr  error
}

func newCollector() *collector {
	return &collector{
		seq:    metrics.NewSequenceAnalyzer(),
		ts:     metrics.NewTimestampAnalyzer(),
		jitter: metrics.NewJitter(),
	}
}

// attachCountExpected records which generated packets should reach the sink
// and the sequence number the stream starts from.
func (c *collector) attachCountExpected(packets []rtpsource.Packet, startSeq uint16) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.skip = make([]bool, len(packets))
	for i, p := range packets {
		c.skip[i] = p.Skip
	}
	c.startSeq = startSeq
}

// attach starts the read loop in the background.
func (c *collector) attach(track *webrtc.TrackRemote) {
	go c.read(track)
}

func (c *collector) read(track *webrtc.TrackRemote) {
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			c.mu.Lock()
			c.readErr = err
			c.mu.Unlock()
			return
		}
		now := time.Now()
		c.mu.Lock()
		first := c.received == 0
		if first && len(c.skip) > 0 {
			// Anchor the expectation on the source's declared start sequence,
			// not on whichever packet happened to arrive first, so losing the
			// head of the stream is counted rather than re-anchored away.
			base := int64(c.startSeq)
			c.expected = make(map[int64]bool, len(c.skip))
			for i, skip := range c.skip {
				if !skip {
					c.expected[base+int64(i)] = true
				}
			}
		}
		c.seq.Add(pkt.SequenceNumber)
		c.ts.Add(pkt.Timestamp)
		c.jitter.Add(pkt.Timestamp, now)
		c.received++
		c.mu.Unlock()
	}
}

// snapshot summarizes what has arrived so far. Lost is computed against the
// plan, so skipped packets never appear as loss.
func (c *collector) snapshot() (metrics.SequenceReport, metrics.TimestampReport, metrics.JitterSummary, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	seq := c.seq.Report()
	if c.expected != nil {
		seen := c.seq.SeenExtensions()
		lost := 0
		for ext := range c.expected {
			if seen[ext] == 0 {
				lost++
			}
		}
		seq.Lost = lost
	}
	return seq, c.ts.Report(), c.jitter.Summary(), c.received, c.readErr
}

// drainWindow bounds how long the egress side may take to catch up after the
// source has finished sending.
const drainWindow = 2 * time.Second

// awaitDrain waits until every planned packet arrives, the stream goes quiet,
// or drainWindow elapses.
func (c *collector) awaitDrain(sched rtpsource.Schedule) {
	want := sched.Generated - sched.Skipped
	deadline := time.Now().Add(drainWindow)
	settle := 250 * time.Millisecond
	var last int
	var quietSince time.Time
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := c.received
		c.mu.Unlock()
		if got >= want {
			break
		}
		if got != last {
			last = got
			quietSince = time.Now()
		} else if !quietSince.IsZero() && time.Since(quietSince) > settle {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
}
