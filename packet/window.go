package packet

// Gap is one contiguous run of missing sequence numbers.
type Gap struct {
	From uint16
	Len  int
}

// BandwidthSample is the sustained receive bandwidth over one bucket.
type BandwidthSample struct {
	BucketMs int64
	Bytes    int
	Kbps     float64
}

// Stats summarizes a reordering pass. Duplicates are never counted as
// loss; loss is reported as contiguous gaps only after the reorder window
// has been exceeded.
type Stats struct {
	Received   int
	Delivered  int
	Duplicates int
	Late       int // arrived after the window closed; not counted as loss again
	Reordered  int // delivered out of order from inside the window
	Gaps       []Gap
	Bandwidth  []BandwidthSample
}

// Lost returns the total number of missing packets across all gaps.
func (s Stats) Lost() int {
	n := 0
	for _, g := range s.Gaps {
		n += g.Len
	}
	return n
}

// MaxBurst returns the longest contiguous loss run.
func (s Stats) MaxBurst() int {
	m := 0
	for _, g := range s.Gaps {
		if g.Len > m {
			m = g.Len
		}
	}
	return m
}

// Reorderer deduplicates and reorders RTP records inside a sliding window.
// Packets missing beyond the window are declared a contiguous gap.
type Reorderer struct {
	Window    int
	BucketMs  int64 // bandwidth sampling bucket; 0 disables
	expected  uint16
	started   bool
	buffered  map[uint16]Record
	delivered map[uint16]bool
	stats     Stats
}

// NewReorderer creates a Reorderer with the given window size.
func NewReorderer(window int) *Reorderer {
	if window < 1 {
		window = 1
	}
	return &Reorderer{
		Window:    window,
		BucketMs:  500,
		buffered:  map[uint16]Record{},
		delivered: map[uint16]bool{},
	}
}

// Push feeds one arrived record in capture order.
func (r *Reorderer) Push(rec Record) {
	r.stats.Received++
	r.addBandwidth(rec)
	if !r.started {
		r.expected = rec.Seq
		r.started = true
	}
	d := int16(rec.Seq - r.expected)
	switch {
	case d == 0:
		r.deliver(rec)
		r.drain(true)
	case d < 0:
		if r.delivered[rec.Seq] {
			r.stats.Duplicates++
		} else {
			r.stats.Late++
		}
	default:
		if _, ok := r.buffered[rec.Seq]; ok {
			r.stats.Duplicates++
			return
		}
		if int(d) >= r.Window {
			// Out of window: everything before rec.Seq that is not
			// buffered is a contiguous loss.
			r.declareGap(rec.Seq)
			r.deliver(rec)
			r.drain(false)
			return
		}
		r.buffered[rec.Seq] = rec
	}
}

// Flush closes the stream: remaining holes become gaps and buffered
// packets are delivered in order.
func (r *Reorderer) Flush() Stats {
	for len(r.buffered) > 0 {
		// All buffered seqs are ahead of expected; find the nearest one.
		var min uint16
		first := true
		for seq := range r.buffered {
			if first || int16(seq-r.expected) < int16(min-r.expected) {
				min, first = seq, false
			}
		}
		r.declareGap(min)
		r.drain(false)
	}
	return r.stats
}

// deliver marks rec consumed and advances the expected sequence.
func (r *Reorderer) deliver(rec Record) {
	r.delivered[rec.Seq] = true
	r.stats.Delivered++
	r.expected = rec.Seq + 1
	// Bound the delivered set to one window behind.
	for seq := range r.delivered {
		if int16(r.expected-seq) > int16(r.Window) {
			delete(r.delivered, seq)
		}
	}
}

// drain delivers consecutive buffered packets. When countReorder is true
// the drain was triggered by the missing predecessor arriving, so every
// drained packet had arrived out of order.
func (r *Reorderer) drain(countReorder bool) {
	for {
		rec, ok := r.buffered[r.expected]
		if !ok {
			return
		}
		delete(r.buffered, r.expected)
		if countReorder {
			r.stats.Reordered++
		}
		r.deliver(rec)
	}
}

// declareGap records loss runs for all unbuffered sequence numbers in
// [expected, upto), delivering buffered packets in between. Each maximal
// contiguous run of missing packets becomes one Gap.
func (r *Reorderer) declareGap(upto uint16) {
	var runStart uint16
	runLen := 0
	flushRun := func() {
		if runLen > 0 {
			r.stats.Gaps = append(r.stats.Gaps, Gap{From: runStart, Len: runLen})
			runLen = 0
		}
	}
	for int16(upto-r.expected) > 0 {
		if _, ok := r.buffered[r.expected]; ok {
			flushRun()
			r.drain(false)
			continue
		}
		if runLen == 0 {
			runStart = r.expected
		}
		runLen++
		r.expected++
	}
	flushRun()
}

func (r *Reorderer) addBandwidth(rec Record) {
	if r.BucketMs <= 0 {
		return
	}
	b := rec.ArrivalMs / r.BucketMs * r.BucketMs
	if n := len(r.stats.Bandwidth); n > 0 && r.stats.Bandwidth[n-1].BucketMs == b {
		s := &r.stats.Bandwidth[n-1]
		s.Bytes += rec.Size
		s.Kbps = float64(s.Bytes) * 8 / float64(r.BucketMs)
		return
	}
	r.stats.Bandwidth = append(r.stats.Bandwidth, BandwidthSample{
		BucketMs: b,
		Bytes:    rec.Size,
		Kbps:     float64(rec.Size) * 8 / float64(r.BucketMs),
	})
}
