package rtprecover

import (
	"encoding/binary"
	"hash/crc32"

	"github.com/medialab/medialab/internal/clock"
	"github.com/pion/rtp"
)

// Envelope is the wire payload layout: 1 flag byte followed by body bytes and
// a big-endian CRC-32 (IEEE) covering flags+body. It lets the corrupt fixture
// produce deterministically checksum-bad packets without external capture.
type Envelope struct {
	Flags byte
	Body  []byte
}

// crcTable is the standard IEEE polynomial used by the self-generated payload
// envelopes; a package-level table avoids per-packet allocation.
var crcTable = crc32.MakeTable(crc32.IEEE)

// Marshal serializes the envelope as flags || body || CRC32(flags||body).
func (e Envelope) Marshal() []byte {
	out := make([]byte, 1+len(e.Body)+4)
	out[0] = e.Flags
	copy(out[1:], e.Body)
	sum := crc32.Checksum(out[:1+len(e.Body)], crcTable)
	binary.BigEndian.PutUint32(out[1+len(e.Body):], sum)
	return out
}

// ParseEnvelope verifies the trailing CRC-32 before trusting any byte inside.
func ParseEnvelope(payload []byte) (Envelope, error) {
	if len(payload) < 5 {
		return Envelope{}, clock.NewError(clock.KindTruncated, "rtprecover", "", 0,
			"payload shorter than envelope header")
	}
	bodyLen := len(payload) - 5
	got := binary.BigEndian.Uint32(payload[1+bodyLen:])
	want := crc32.Checksum(payload[:1+bodyLen], crcTable)
	if got != want {
		return Envelope{}, clock.NewError(clock.KindChecksum, "rtprecover", "", 0,
			"envelope CRC-32 mismatch")
	}
	body := make([]byte, bodyLen)
	copy(body, payload[1:1+bodyLen])
	return Envelope{Flags: payload[0], Body: body}, nil
}

// Gap is a confirmed missing sequence span with its timeline time range.
type Gap struct {
	TrackID  string
	StartSeq uint16
	EndSeq   uint16 // inclusive
	Ticks    clock.Range
	Length   int
}

// RecoverConfig parameterizes burst detection and reorder behavior.
type RecoverConfig struct {
	WindowCapacity int
	BurstThreshold int // consecutive missing seqs that constitute a reportable gap
	TrackID        string
	IsVideo        bool // video waits for a keyframe after a gap
}

// Recover wraps a Window with checksum verification, burst-gap detection and
// keyframe-gated resumption. All buffering stays inside the bounded window.
type Recover struct {
	cfg        RecoverConfig
	win        *Window
	waitingKF  bool
	gaps       []Gap
	tickOfSeq  func(uint16) int64 // deterministic seq -> manifest tick mapping
	lastTick   int64
	lastSeq    uint16 // sequence number of the most recently emitted sample
	maxObsSeq  uint16 // highest sequence number ever accepted
	haveLast   bool
	haveObsMax bool
}

// NewRecover builds a Recover. tickOf maps a sequence number to its manifest
// timeline tick so gap ranges carry real time positions.
func NewRecover(cfg RecoverConfig, tickOf func(uint16) int64) *Recover {
	if cfg.BurstThreshold <= 0 {
		cfg.BurstThreshold = 1
	}
	return &Recover{
		cfg:       cfg,
		win:       NewWindow(cfg.WindowCapacity),
		tickOfSeq: tickOf,
	}
}

// Push verifies and inserts one packet. Checksum-bad packets are counted but
// never enter the window, so they surface as real gaps instead of garbage.
func (r *Recover) Push(pkt *rtp.Packet) error {
	// Every observed seq moves the high-water mark, including rejected ones,
	// so end-of-stream can still see the tail gap they leave behind.
	r.noteObserved(pkt.SequenceNumber)
	env, err := ParseEnvelope(pkt.Payload)
	if err != nil {
		r.win.stats.ChecksumBad++
		return err
	}
	if dropped, kind := r.win.Push(pkt); dropped {
		if kind == DropDuplicate {
			return clock.NewError(clock.KindChecksum, "rtprecover", r.cfg.TrackID,
				int64(pkt.SequenceNumber), "duplicate sequence dropped")
		}
		return clock.NewError(clock.KindLate, "rtprecover", r.cfg.TrackID,
			int64(pkt.SequenceNumber), "packet outside reorder window")
	}
	if env.Flags&FlagKeyframe != 0 {
		r.waitingKF = false
	}
	return nil
}

// noteObserved advances the high-water sequence mark across wrap boundaries.
func (r *Recover) noteObserved(seq uint16) {
	if !r.haveObsMax || seqDistance(r.maxObsSeq, seq) > 0 {
		r.maxObsSeq = seq
		r.haveObsMax = true
	}
}

// Drain emits every currently orderable sample, attaching gap markers when a
// burst of at least BurstThreshold consecutive packets is missing.
func (r *Recover) Drain() ([]Sample, error) {
	var out []Sample
	flushErr := r.win.Flush(func(pkt *rtp.Packet) error {
		s, err := r.makeSample(pkt)
		if err != nil {
			return err
		}
		out = append(out, s)
		return nil
	})
	if flushErr != nil {
		return nil, flushErr
	}
	more, err := r.advanceHoles()
	if err != nil {
		return nil, err
	}
	return append(out, more...), nil
}

// advanceHoles skips confirmed holes at the head. A hole is confirmed only
// once a real packet behind it sits in the window, proving the missing
// sequence numbers are lost rather than merely reordered. Sub-threshold runs
// keep waiting; threshold-sized runs become a Gap and are rotated past
// without producing any synthetic frames (video then awaits a keyframe).
func (r *Recover) advanceHoles() ([]Sample, error) {
	var out []Sample
	for {
		first := r.win.FirstFilled()
		if first <= 0 {
			return out, nil // empty window (nothing observed) or head is ready
		}
		if first < r.cfg.BurstThreshold {
			return out, nil // keep buffering; reorder may still fill the hole
		}
		head, _ := r.win.Head()
		g := Gap{
			TrackID:  r.cfg.TrackID,
			StartSeq: head,
			EndSeq:   head + uint16(first) - 1,
			Ticks: clock.Range{
				Start: r.tickOfSeq(head),
				End:   r.tickOfSeq(head+uint16(first)) + 1,
			},
			Length: first,
		}
		for i := 0; i < first; i++ {
			r.win.SkipHead() // never fabricate the missing packet
		}
		r.gaps = append(r.gaps, g)
		if r.cfg.IsVideo {
			r.waitingKF = true
		}
		if err := r.win.Flush(func(pkt *rtp.Packet) error {
			s, err := r.makeSample(pkt)
			if err != nil {
				return err
			}
			out = append(out, s)
			return nil
		}); err != nil {
			return nil, err
		}
	}
}

// Close flushes at end-of-stream. Packets still missing at this point are
// confirmed gaps (their packets will never arrive) following the same
// no-fabrication rules as mid-stream holes.
func (r *Recover) Close() ([]Sample, error) {
	var out []Sample
	more, err := r.advanceHoles()
	if err != nil {
		return nil, err
	}
	out = append(out, more...)
	// A tail loss can leave the window empty, so FirstFilled cannot see it;
	// the still-missing expectation is derived from the highest accepted seq.
	if r.haveObsMax {
		want := r.maxObsSeq + 1
		head, started := r.win.Head()
		if started && seqDistance(head, want) > 0 {
			length := int(seqDistance(head, want))
			g := Gap{
				TrackID:  r.cfg.TrackID,
				StartSeq: head,
				EndSeq:   want - 1,
				Ticks: clock.Range{
					Start: r.tickOfSeq(head),
					End:   r.tickOfSeq(want) + 1,
				},
				Length: length,
			}
			for i := 0; i < length; i++ {
				r.win.SkipHead()
			}
			r.gaps = append(r.gaps, g)
			if r.cfg.IsVideo {
				r.waitingKF = true
			}
		}
	}
	if first := r.win.FirstFilled(); first > 0 {
		head, _ := r.win.Head()
		g := Gap{
			TrackID:  r.cfg.TrackID,
			StartSeq: head,
			EndSeq:   head + uint16(first) - 1,
			Ticks: clock.Range{
				Start: r.tickOfSeq(head),
				End:   r.tickOfSeq(head+uint16(first)) + 1,
			},
			Length: first,
		}
		for i := 0; i < first; i++ {
			r.win.SkipHead()
		}
		r.gaps = append(r.gaps, g)
		if r.cfg.IsVideo {
			r.waitingKF = true
		}
	}
	if err := r.win.Flush(func(pkt *rtp.Packet) error {
		s, err := r.makeSample(pkt)
		if err != nil {
			return err
		}
		out = append(out, s)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Recover) makeSample(pkt *rtp.Packet) (Sample, error) {
	env, err := ParseEnvelope(pkt.Payload)
	if err != nil {
		return Sample{}, err
	}
	tick := r.tickOfSeq(pkt.SequenceNumber)
	if r.haveLast && tick <= r.lastTick {
		return Sample{}, clock.NewError(clock.KindNonMonotonic, "rtprecover",
			r.cfg.TrackID, tick, "recovered sample time not monotonic")
	}
	s := Sample{
		Seq:       pkt.SequenceNumber,
		TrackID:   r.cfg.TrackID,
		AtTick:    tick,
		Keyframe:  env.Flags&FlagKeyframe != 0,
		WaitingKF: r.waitingKF && !pktHasKeyframe(env.Flags),
		Flags:     env.Flags,
		Body:      env.Body,
	}
	if s.Keyframe {
		s.WaitingKF = false
	}
	r.lastTick, r.lastSeq, r.haveLast = tick, pkt.SequenceNumber, true
	return s, nil
}

func pktHasKeyframe(flags byte) bool { return flags&FlagKeyframe != 0 }

// Gaps returns a copy of all confirmed burst gaps.
func (r *Recover) Gaps() []Gap {
	out := make([]Gap, len(r.gaps))
	copy(out, r.gaps)
	return out
}

// Stats merges window counters with gap information.
func (r *Recover) Stats() Stats {
	st := r.win.Stats()
	return st
}
