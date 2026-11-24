// Package clock unifies RTP timestamp unwrapping, RTCP sender-report
// clock mapping and the shared 48 kHz media time base.
package clock

// MediaClockHz is the single output time base used by every pipeline stage.
const MediaClockHz uint32 = 48000

const (
	wrapMod32 uint64 = 1 << 32
	wrapMod16 uint64 = 1 << 16
)

// SeqUnwrapper expands 16-bit RTP sequence numbers into a monotonic uint64
// counter that survives the 65536-sample wrap.
type SeqUnwrapper struct {
	last    uint64
	started bool
}

// Update feeds the next raw sequence number and returns its extended value.
// Inputs are expected in arrival order; backwards jumps smaller than half the
// modulo are treated as wrapped/duplicate inputs and resolved by shortest path.
func (s *SeqUnwrapper) Update(raw uint16) uint64 {
	r := uint64(raw)
	if !s.started {
		s.started = true
		s.last = r
		return r
	}
	delta := int64(r-(s.last%wrapMod16))
	if delta < -int64(wrapMod16/2) {
		delta += int64(wrapMod16)
	} else if delta > int64(wrapMod16/2) {
		delta -= int64(wrapMod16)
	}
	s.last = uint64(int64(s.last) + delta)
	return s.last
}

// TSUnwrapper expands 32-bit RTP timestamps into uint64 ticks in the source
// clock domain using the same shortest-path rule as the sequence unwrapper.
type TSUnwrapper struct {
	last    uint64
	started bool
}

// Update feeds a raw RTP timestamp and returns the unwrapped tick count.
func (t *TSUnwrapper) Update(raw uint32) uint64 {
	r := uint64(raw)
	if !t.started {
		t.started = true
		t.last = r
		return r
	}
	delta := int64(r - (t.last % wrapMod32))
	if delta < -int64(wrapMod32/2) {
		delta += int64(wrapMod32)
	} else if delta > int64(wrapMod32/2) {
		delta -= int64(wrapMod32)
	}
	t.last = uint64(int64(t.last) + delta)
	return t.last
}

// NTPToNanos converts a 64-bit NTP timestamp (32.32 fixed point) into
// nanoseconds. Only relative differences are meaningful, which is all the
// clock mapping needs.
func NTPToNanos(ntp uint64) int64 {
	secs := ntp >> 32
	frac := ntp & 0xffffffff
	return int64(secs)*1_000_000_000 + int64(frac*1_000_000_000>>32)
}

// ClockMap maps one source clock rate onto the 48 kHz media time base.
// Late RTCP sender reports refine the mapping; positions already committed
// are frozen and the mapping is clamped so the timeline never regresses.
type ClockMap struct {
	rate           uint32
	anchored       bool
	baseRTP        uint64 // unwrapped source tick corresponding to baseMedia
	baseMedia      uint64 // media ticks (48 kHz) for baseRTP
	committedMedia uint64
	haveSR         bool
	anchorNTP      int64
	anchorRTP      uint64
	anchorMedia    uint64
}

// NewClockMap creates a map for the given source sample rate. The first
// packet establishes a provisional zero-based anchor until an SR arrives.
func NewClockMap(rate uint32) *ClockMap {
	if rate == 0 {
		rate = MediaClockHz
	}
	return &ClockMap{rate: rate}
}

// mapLocked converts unwrapped source ticks to 48 kHz media ticks.
func (c *ClockMap) mapLocked(unwrapped uint64) uint64 {
	elapsed := int64(unwrapped - c.baseRTP)
	media := int64(c.baseMedia) + elapsed*int64(MediaClockHz)/int64(c.rate)
	if media < 0 {
		media = 0
	}
	if uint64(media) < c.committedMedia {
		media = int64(c.committedMedia) // never move committed time backwards
	}
	return uint64(media)
}

// Position returns the media position for an unwrapped source timestamp.
func (c *ClockMap) Position(unwrapped uint64) uint64 {
	if !c.anchored {
		c.anchored = true
		c.baseRTP = unwrapped // provisional anchor: first packet is t=0
	}
	return c.mapLocked(unwrapped)
}

// ApplySR incorporates a (possibly late) RTCP sender report. The NTP/RTP pair
// establishes wall-clock alignment; comparing against the previous SR also
// absorbs source clock drift because elapsed wall time, not nominal rate,
// drives the corrected position.
func (c *ClockMap) ApplySR(unwrappedRTP uint64, ntp uint64) {
	when := NTPToNanos(ntp)
	if !c.haveSR {
		c.haveSR = true
		c.anchorNTP, c.anchorRTP, c.anchorMedia = when, unwrappedRTP, c.mapLocked(unwrappedRTP)
		c.baseRTP, c.baseMedia = c.anchorRTP, c.anchorMedia
		return
	}
	mediaAtSR := c.anchorMedia + uint64((when-c.anchorNTP)*int64(MediaClockHz)/1_000_000_000)
	if mediaAtSR < c.committedMedia {
		mediaAtSR = c.committedMedia
	}
	// Re-anchor: unwrappedRTP now corresponds to mediaAtSR.
	c.baseRTP, c.baseMedia = unwrappedRTP, mediaAtSR
}

// Freeze marks every media position up to the given one immutable.
func (c *ClockMap) Freeze(mediaPos uint64) {
	if mediaPos > c.committedMedia {
		c.committedMedia = mediaPos
	}
}

// PacketRef is the minimal packet information the timeline consumes.
type PacketRef struct {
	Seq       uint16 // raw RTP sequence number; extension happens here
	RTPTS     uint32
	ClockRate uint32
	Offset    int64 // raw byte offset in the input stream
}

// TimelineEntry is one ordered packet placed on the unified time base.
type TimelineEntry struct {
	ExtSeq   uint64
	MediaPos uint64 // 48 kHz ticks
	Offset   int64
}

// Timeline orders PacketRefs by extended sequence number and assigns each a
// monotonic 48 kHz media position.
type Timeline struct {
	seq          *SeqUnwrapper
	ts           *TSUnwrapper
	maps         map[uint32]*ClockMap
	entries      []TimelineEntry
	committedSeq uint64
	lastMedia    uint64
}

// NewTimeline builds an empty timeline.
func NewTimeline() *Timeline {
	return &Timeline{
		seq:  &SeqUnwrapper{},
		ts:   &TSUnwrapper{},
		maps: map[uint32]*ClockMap{},
	}
}

// Add places one packet. ExtSeq is assigned here; the packet must arrive in
// reorder-window output order. The returned position never decreases.
func (t *Timeline) Add(ref PacketRef) TimelineEntry {
	extSeq := t.seq.Update(ref.Seq)
	unwrappedTS := t.ts.Update(ref.RTPTS)
	cm, ok := t.maps[ref.ClockRate]
	if !ok {
		cm = NewClockMap(ref.ClockRate)
		t.maps[ref.ClockRate] = cm
	}
	media := cm.Position(unwrappedTS)
	if media < t.lastMedia {
		media = t.lastMedia
	}
	e := TimelineEntry{ExtSeq: extSeq, MediaPos: media, Offset: ref.Offset}
	t.entries = append(t.entries, e)
	t.lastMedia = media
	return e
}

// ApplySenderReport forwards a late SR to the matching clock map.
func (t *Timeline) ApplySenderReport(rate uint32, rtpTS uint32, ntp uint64) {
	cm, ok := t.maps[rate]
	if !ok {
		cm = NewClockMap(rate)
		t.maps[rate] = cm
	}
	cm.ApplySR(t.ts.Update(rtpTS), ntp)
}

// Commit freezes every entry whose sequence is at or below upToSeq.
func (t *Timeline) Commit(upToSeq uint64) {
	if upToSeq <= t.committedSeq {
		return
	}
	t.committedSeq = upToSeq
	for i := len(t.entries) - 1; i >= 0; i-- {
		if t.entries[i].ExtSeq <= upToSeq {
			t.Freeze(t.entries[i].MediaPos)
			break
		}
	}
}

// Freeze marks a media position immutable across every clock map.
func (t *Timeline) Freeze(mediaPos uint64) {
	for _, cm := range t.maps {
		cm.Freeze(mediaPos)
	}
	if mediaPos > t.lastMedia {
		t.lastMedia = mediaPos
	}
}

// Entries returns the timeline entries accumulated so far.
func (t *Timeline) Entries() []TimelineEntry { return t.entries }

// CommittedSeq returns the highest committed extended sequence number.
func (t *Timeline) CommittedSeq() uint64 { return t.committedSeq }
