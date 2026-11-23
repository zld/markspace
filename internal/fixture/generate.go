// Package fixture generates fully deterministic local sample data: RTP
// packet streams, SRT/WebVTT subtitle tracks and bandwidth samples. Nothing
// here reads external files; every output is a pure function of the seed,
// which is what makes repeated runs byte-identical.
package fixture

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"math/rand"

	"github.com/pion/rtp"

	"mediaverify/internal/clock"
)

// PayloadSize is the deterministic per-packet payload length.
const PayloadSize = 1200

// SSRC used by the generated single-program stream.
const SSRC uint32 = 0x4D56_3031 // "MV01"

// Fault names supported by the generator.
const (
	FaultNone              = ""
	FaultBurstLoss         = "burst-loss"
	FaultBadChecksum       = "bad-checksum"
	FaultTimestampRollback = "timestamp-rollback"
	FaultBoundaryLate      = "boundary-late"
	FaultEmptySubtitle     = "empty-subtitle"
	FaultBandwidthDrop     = "bandwidth-drop"
)

// Config describes the requested sample asset.
type Config struct {
	Seed      int64
	Duration  clock.Ticks
	FrameRate clock.FrameRate
	Fault     string
	TargetBPS int64 // nominal bandwidth the samples orbit
}

// Stream is the generated asset.
type Stream struct {
	Packets       []*rtp.Packet // arrival order (faults already applied)
	IdealSeq      []uint16      // sequence numbers before loss/reordering
	Keyframes     map[uint16]bool
	Bandwidth     []BandwidthPoint
	SRT           string
	VTT           string
	InputDigest   string
	Frames        int
	ClockRate     clock.Rational
	TicksPerFrame int64 // RTP clock ticks per video frame
	// LatePacket is held out of the arrival stream and delivered after the
	// watermark passes its event time (boundary-late fault only).
	LatePacket *rtp.Packet
	LateSeq16  uint16
	HasLate    bool
}

// BandwidthPoint is one per-second bandwidth observation.
type BandwidthPoint struct {
	Time  clock.Ticks
	Bytes uint64
}

// Generate builds the deterministic stream.
func Generate(cfg Config) (*Stream, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewSource(cfg.Seed))
	frameDur := cfg.FrameRate.FrameDurationNS()
	frames := int(int64(cfg.Duration) / int64(frameDur))
	rate := clock.Rational{Num: 1, Den: 90000}
	ticksPerFrame, _ := clock.TicksToScale(frameDur, rate)

	s := &Stream{
		Keyframes:     map[uint16]bool{},
		Frames:        frames,
		ClockRate:     rate,
		TicksPerFrame: ticksPerFrame,
	}
	// Ideal packets and digest over their canonical content.
	digest := sha256.New()
	type ideal struct {
		pkt *rtp.Packet
	}
	ideals := make([]ideal, frames)
	for i := 0; i < frames; i++ {
		payload := makePayload(rng, uint32(i))
		seq := uint16(i)
		keyframe := i%int(cfg.FrameRate.Nominal) == 0
		if keyframe {
			s.Keyframes[seq] = true
		}
		p := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SSRC:           SSRC,
				SequenceNumber: seq,
				Timestamp:      uint32(int64(i) * ticksPerFrame),
				Marker:         keyframe,
			},
			Payload: payload,
		}
		ideals[i] = ideal{pkt: p}
		s.IdealSeq = append(s.IdealSeq, seq)
		binary.Write(digest, binary.BigEndian, seq)
		binary.Write(digest, binary.BigEndian, p.Timestamp)
		digest.Write(payload)
	}
	s.InputDigest = hex.EncodeToString(digest.Sum(nil))

	for _, id := range ideals {
		s.Packets = append(s.Packets, id.pkt)
	}
	s.Bandwidth = generateBandwidth(cfg, rng, frames, frameDur)
	s.SRT, s.VTT = generateSubtitles(cfg, frameDur, frames)

	applyFault(cfg, s, rng)
	return s, nil
}

// makePayload fills a packet with deterministic bytes and a trailing CRC32
// over the preceding content. ChecksumCRC exposes the same algorithm to the
// verification stage.
func makePayload(rng *rand.Rand, idx uint32) []byte {
	buf := make([]byte, PayloadSize)
	binary.BigEndian.PutUint32(buf[0:4], idx)
	for i := 4; i < PayloadSize-4; i++ {
		buf[i] = byte(rng.Intn(256))
	}
	crc := crc32.ChecksumIEEE(buf[:PayloadSize-4])
	binary.BigEndian.PutUint32(buf[PayloadSize-4:], crc)
	return buf
}

// ChecksumCRC returns the CRC carried in the last four payload bytes.
func ChecksumCRC(payload []byte) (stored, computed uint32, ok bool) {
	if len(payload) < 5 {
		return 0, 0, false
	}
	stored = binary.BigEndian.Uint32(payload[len(payload)-4:])
	computed = crc32.ChecksumIEEE(payload[:len(payload)-4])
	return stored, computed, stored == computed
}

func validate(cfg Config) error {
	if cfg.Duration <= 0 {
		return errInvalid("duration must be positive")
	}
	if cfg.Duration > 2*clock.Ticks(3600_000_000_000) {
		return errLimit("asset exceeds the 2-hour limit")
	}
	switch cfg.Fault {
	case FaultNone, FaultBurstLoss, FaultBadChecksum, FaultTimestampRollback,
		FaultBoundaryLate, FaultEmptySubtitle, FaultBandwidthDrop:
	default:
		return errInvalid("unknown fault %q", cfg.Fault)
	}
	return nil
}
