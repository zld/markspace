package fixture

import (
	"fmt"
	"math/rand"

	"github.com/pion/rtp"

	"mediaverify/internal/clock"
	"mediaverify/internal/errs"
)

func errInvalid(format string, args ...any) error {
	return errs.New(errs.KindInvalidInput, "fixture", format, args...)
}

func errLimit(format string, args ...any) error {
	return errs.New(errs.KindLimitExceeded, "fixture", format, args...)
}

// generateBandwidth produces per-second byte counts that orbit TargetBPS
// with smooth variation, plus a hard cliff under the bandwidth-drop fault.
func generateBandwidth(cfg Config, rng *rand.Rand, frames int, frameDur clock.Ticks) []BandwidthPoint {
	seconds := int(int64(cfg.Duration) / 1_000_000_000)
	if seconds == 0 {
		seconds = 1
	}
	baseBytes := uint64(cfg.TargetBPS / 8)
	out := make([]BandwidthPoint, 0, seconds)
	for sec := 1; sec <= seconds; sec++ {
		// Deterministic wave +-25% so the estimator sees sustained change.
		wave := 1.0 + 0.25*sinApprox(float64(sec)/3.0)
		jitter := 1.0 + (rng.Float64()-0.5)*0.04
		bps := float64(baseBytes) * 8 * wave * jitter
		if cfg.Fault == FaultBandwidthDrop && sec >= seconds/2 {
			bps *= 0.35 // cliff well below the lowest rung
		}
		out = append(out, BandwidthPoint{
			Time:  clock.Nanoseconds(int64(sec) * 1_000_000_000),
			Bytes: uint64(bps / 8),
		})
	}
	return out
}

// sinApprox avoids pulling math in for a deterministic bounded wave.
func sinApprox(x float64) float64 {
	// Periodic triangular-ish curve in [-1,1] using modulo arithmetic.
	y := x - float64(int64(x))
	if y < 0.5 {
		return 4*y - 1
	}
	return 3 - 4*y
}

// generateSubtitles builds matching SRT and WebVTT text; the empty-subtitle
// fault emits a structurally present but body-less cue for the parser test.
func generateSubtitles(cfg Config, frameDur clock.Ticks, frames int) (string, string) {
	end := int64(frameDur) * int64(frames)
	cueEvery := int64(1_500_000_000)
	type cue struct {
		start, end int64
		text       string
	}
	var cues []cue
	idx := 1
	for start := int64(0); start+1_000_000_000 < end; start += cueEvery {
		cues = append(cues, cue{start, start + 1_200_000_000,
			fmt.Sprintf("program cue %d", idx)})
		idx++
	}
	srt := ""
	vtt := "WEBVTT\n"
	for i, c := range cues {
		srt += fmt.Sprintf("%d\n%s --> %s\n%s\n\n", i+1, srtClock(c.start), srtClock(c.end), c.text)
		vtt += fmt.Sprintf("\n%d\n%s --> %s\n%s\n", i+1, vttClock(c.start), vttClock(c.end), c.text)
	}
	if cfg.Fault == FaultEmptySubtitle {
		// Deliberately invalid: timing line with no body.
		srt += fmt.Sprintf("%d\n%s --> %s\n\n", len(cues)+1, srtClock(0), srtClock(1_000_000_000))
	}
	return srt, vtt
}

func srtClock(ns int64) string {
	return clockFmt(ns, ',')
}

func vttClock(ns int64) string {
	return clockFmt(ns, '.')
}

func clockFmt(ns int64, sep byte) string {
	ms := ns / 1_000_000
	h := ms / 3_600_000
	ms %= 3_600_000
	m := ms / 60_000
	ms %= 60_000
	s := ms / 1000
	ms %= 1000
	return fmt.Sprintf("%02d:%02d:%02d%c%03d", h, m, s, sep, ms)
}

// applyFault mutates the arrival-order packet slice deterministically.
func applyFault(cfg Config, s *Stream, rng *rand.Rand) {
	switch cfg.Fault {
	case FaultBurstLoss:
		// Drop a contiguous burst near the middle spanning a keyframe.
		start := len(s.Packets) / 2
		key := nearestKeyframe(s, start)
		lo, hi := key-1, key+int(cfg.FrameRate.Nominal)/2
		kept := s.Packets[:0]
		for i, p := range s.Packets {
			if i >= lo && i <= hi {
				continue
			}
			kept = append(kept, p)
		}
		s.Packets = kept
	case FaultBadChecksum:
		p := s.Packets[len(s.Packets)/3]
		p.Payload[4] ^= 0xFF // corrupt content, leaving the stored CRC stale
	case FaultTimestampRollback:
		p := s.Packets[len(s.Packets)/2]
		p.Timestamp -= 90_000 // one second backwards without a wrap
	case FaultBoundaryLate:
		// Hold one mid-stream packet out of the arrival order. Ingest
		// delivers it only after the watermark has advanced past it,
		// exercising the deterministic late-event replay path.
		i := len(s.Packets)/2 - 2
		s.LatePacket = s.Packets[i]
		s.LateSeq16 = s.LatePacket.SequenceNumber
		s.HasLate = true
		s.Packets = append(s.Packets[:i], s.Packets[i+1:]...)
	}
	_ = rng
}

func nearestKeyframe(s *Stream, idx int) int {
	for d := 0; idx+d < len(s.Packets); d++ {
		if s.Keyframes[s.Packets[idx+d].SequenceNumber] {
			return idx + d
		}
		if idx-d >= 0 && s.Keyframes[s.Packets[idx-d].SequenceNumber] {
			return idx - d
		}
	}
	return idx
}

// PacketAt is a test helper returning the packet with a given sequence.
func (s *Stream) PacketAt(seq uint16) *rtp.Packet {
	for _, p := range s.Packets {
		if p.SequenceNumber == seq {
			return p
		}
	}
	return nil
}
