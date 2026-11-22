// Package fixture generates all synthetic inputs deterministically from a
// seed: RTP streams with injected network faults, SRT/WebVTT cues, timecode
// labels, audio/color metadata and MP4 sample tables. No external files.
package fixture

import (
	"fmt"

	"mediapipeline/internal/rtp"
)

// Fault identifies one of the five self-generated fault classes the pipeline
// must locate and handle.
type Fault string

const (
	FaultRTPDuplicate      Fault = "rtp-duplicate"
	FaultRTPReorder        Fault = "rtp-reorder"
	FaultLossAcrossSubtitle Fault = "loss-across-subtitle"
	FaultDFIllegalFrame    Fault = "df-illegal-frame"
	FaultAudioConflict     Fault = "audio-conflict"
	FaultStageInterrupt    Fault = "stage-interrupt"
)

// AllFaults lists the five mandated fault classes (interrupt is driven by the
// CLI, so the generator itself produces the first five plus its marker).
func AllFaults() []Fault {
	return []Fault{FaultRTPDuplicate, FaultRTPReorder, FaultLossAcrossSubtitle,
		FaultDFIllegalFrame, FaultAudioConflict, FaultStageInterrupt}
}

// Config controls one deterministic generation run.
type Config struct {
	Seed       int64
	PacketN    int    // packets per stream
	JitterMS   int    // synthetic arrival jitter window
	LossPct    int    // whole-percent packet loss
	SSRC       uint32
	PT         uint8
	ClockRate  uint32
	// FirstBadSeq is the wire sequence number of the first injected fault;
	// acceptance checks locate against this anchor (~1000).
	FirstBadSeq uint16
}

// DefaultConfig matches the acceptance demo parameters.
func DefaultConfig(seed int64) Config {
	return Config{
		Seed: seed, PacketN: 2000, JitterMS: 80, LossPct: 3,
		SSRC: 0x42424242, PT: 96, ClockRate: 48000, FirstBadSeq: 1000,
	}
}

// rng is a tiny deterministic xorshift generator so fixture bytes depend only
// on the seed, never on math/rand global state or wall time.
type rng struct{ state uint64 }

func newRNG(seed int64) *rng {
	s := uint64(seed)
	if s == 0 {
		s = 0x9e3779b97f4a7c15
	}
	return &rng{state: s}
}

func (r *rng) next() uint64 {
	x := r.state
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	r.state = x
	return x
}

func (r *rng) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}

// Stream is one generated RTP track plus a manifest of injected faults.
type Stream struct {
	Packets    []rtp.Packet
	Dropped    []uint16 // wire sequence numbers intentionally dropped
	Duplicates []uint16
	Reordered  []uint16
	FirstBad   uint16
}

// GenerateStream builds PacketN packets in wire order, then applies jitter,
// duplicate/reorder/loss faults starting at FirstBadSeq. The returned packet
// slice is in (faulty) arrival order; sequence numbers stay on the wire.
func GenerateStream(c Config) Stream {
	r := newRNG(c.Seed)
	out := Stream{FirstBad: c.FirstBadSeq}
	nanos, per, _ := rtp.ClockRate(c.ClockRate)
	all := make([]rtp.Packet, 0, c.PacketN)
	for i := 0; i < c.PacketN; i++ {
		seq := uint16(i)
		ticks := uint32(i) * 160 // fixed 160 audio samples per packet at 48k
		p := rtp.Packet{
			SSRC:    c.SSRC,
			PT:      c.PT,
			Seq:     seq,
			RTPTime: ticks,
			Nanos:   rtp.RTPToNanos(ticks, nanos, per),
			Payload: []byte{byte(r.next()), byte(r.next() >> 8)},
		}
		all = append(all, p)
	}
	// Faults apply only at/after FirstBadSeq so early packets stay clean.
	badStart := int(c.FirstBadSeq)
	if badStart >= len(all) {
		badStart = len(all)
	}
	// 1) Random loss first, with every fault anchor protected so the injected
	// faults below cannot be silently removed by the stochastic loss filter.
	protected := protectedSeqs(c)
	kept := make([]rtp.Packet, 0, len(all))
	for _, p := range all {
		if !protected[p.Seq] && int(p.Seq) >= badStart && r.intn(100) < c.LossPct {
			out.Dropped = append(out.Dropped, p.Seq)
			continue
		}
		kept = append(kept, p)
	}
	arrival := kept
	// 2) Deterministic bounded jitter (odd-even transposition; reorder depth
	// never exceeds the number of passes, so it stays inside the buffer window).
	jitter := c.JitterMS
	if jitter > 32 {
		jitter = 32
	}
	for pass := 0; pass < jitter; pass++ {
		start := pass & 1
		for i := start; i+1 < len(arrival); i += 2 {
			if r.next()&1 == 1 {
				arrival[i], arrival[i+1] = arrival[i+1], arrival[i]
			}
		}
	}
	// 3) Inject the protected duplicate and reorder faults by locating the
	// anchor packets in their (jittered) arrival positions.
	dupPos := indexOfSeq(arrival, c.FirstBadSeq)
	if dupPos >= 0 {
		out.Duplicates = []uint16{arrival[dupPos].Seq}
		arrival = insertCopy(arrival, dupPos)
	}
	if rp := indexOfSeq(arrival, c.FirstBadSeq+4); rp >= 0 {
		rp2 := indexOfSeq(arrival, c.FirstBadSeq+2)
		if rp2 >= 0 {
			arrival[rp], arrival[rp2] = arrival[rp2], arrival[rp]
			out.Reordered = []uint16{arrival[rp].Seq, arrival[rp2].Seq}
		}
	}
	out.Packets = arrival
	return out
}

// protectedSeqs lists every wire sequence that an injected fault depends on,
// so random loss can never remove them.
func protectedSeqs(c Config) map[uint16]bool {
	return map[uint16]bool{
		c.FirstBadSeq:                       true, // duplicate anchor
		c.FirstBadSeq + 2:                   true, // reorder pair
		c.FirstBadSeq + 4:                   true,
		SubtitleSyncSeq - 1:                 true, // contiguous loss group
		SubtitleSyncSeq:                     true,
		SubtitleSyncSeq + 1:                 true,
	}
}

func indexOfSeq(pkts []rtp.Packet, seq uint16) int {
	for i, p := range pkts {
		if p.Seq == seq {
			return i
		}
	}
	return -1
}

func insertCopy(pkts []rtp.Packet, at int) []rtp.Packet {
	cp := make([]rtp.Packet, 0, len(pkts)+1)
	cp = append(cp, pkts[:at]...)
	cp = append(cp, pkts[at])
	cp = append(cp, pkts[at:]...)
	return cp
}

// SubtitleSyncSeq is the extended sequence position that straddles a subtitle
// cue anchor; the contiguous-loss fault is centered on it.
const SubtitleSyncSeq = 1010

// GenerateLossAcrossSubtitle forces three consecutive drops around
// SubtitleSyncSeq so the loss crosses the subtitle synchronization point.
func GenerateLossAcrossSubtitle(c Config) Stream {
	s := GenerateStream(c)
	want := map[uint16]bool{
		SubtitleSyncSeq - 1: true,
		SubtitleSyncSeq:     true,
		SubtitleSyncSeq + 1: true,
	}
	kept := make([]rtp.Packet, 0, len(s.Packets))
	for _, p := range s.Packets {
		if want[p.Seq] {
			s.Dropped = append(s.Dropped, p.Seq)
			continue
		}
		kept = append(kept, p)
	}
	s.Packets = kept
	return s
}

// CueText renders deterministic SRT and WebVTT documents whose sync point sits
// at the same shared-base position as SubtitleSyncSeq.
func CueText() (srt, vtt string) {
	// 1010 packets * 160 samples / 48000 Hz ~= 3.3667s => cue near 3.4s.
	srt = "1\n00:00:03,366 --> 00:00:05,000\nsync point cue\n\n" +
		"2\n00:00:06,000 --> 00:00:07,500\nsecond cue\n"
	vtt = "WEBVTT\n\n00:03.366 --> 00:05.000\nsync point cue\n\n00:06.000 --> 00:07.500\nsecond cue\n"
	return srt, vtt
}

// IllegalDFLabel returns a drop-frame label that is illegal (frame 0 of a
// non-ten minute).
func IllegalDFLabel() string { return "00:01:00:00" }

// AudioConflictConfig returns a channel/sample-rate plan guaranteed to
// conflict: 5.1 source declared as stereo with no mapping matrix.
func AudioConflictConfig() string {
	return fmt.Sprintf("layout=5.1 declared=stereo matrix=none")
}
