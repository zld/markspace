// Package media generates the self-contained synthetic fixtures used by the
// acceptance demo and by tests: length-prefixed frame records and RTP audio
// packets. No external media files are referenced. It also computes the
// quality and bitrate metrics, all on the canonical nanosecond time base.
package media

import (
	"bytes"
	"encoding/binary"
	"math"

	"github.com/pion/rtp"

	"example.com/continuity/internal/frame"
)

// Spec describes a generated fixture set.
type Spec struct {
	Frames int
	Width  uint32
	Height uint32
	FPS    int64
	// DuplicateAfter makes frames from that index onward repeat an earlier
	// frame's normalized content to exercise fingerprint dedup.
	DuplicateAfter int
	// SilenceStart/SilenceEnd define an inclusive frame range rendered as
	// silent audio (audio packets are generated per frame).
	SilenceStart int
	SilenceEnd   int
	// JumpAt inserts an inter-frame timestamp jump at this frame index.
	JumpAt      int
	JumpExtraNS int64
	// DupJumpAt inserts a jump where the duplicate-content block begins so
	// it is cut into its own segment for fingerprint matching.
	DupJumpAt      int
	DupJumpExtraNS int64
}

// DefaultSpec returns the standard acceptance fixture specification.
func DefaultSpec() Spec {
	return Spec{
		Frames: 120, Width: 32, Height: 24, FPS: 25,
		DuplicateAfter: 96, // last 24 frames repeat the first block's content
		SilenceStart:   40,
		SilenceEnd:     59, // 20 frames of silence
		JumpAt:         72,
		JumpExtraNS:    200_000_000,
		// A second jump at the start of the duplicate block makes it a
		// standalone segment whose content matches an earlier segment.
		DupJumpAt:      96,
		DupJumpExtraNS: 200_000_000,
	}
}

// FrameIntervalNS returns the nominal inter-frame interval in nanoseconds.
func (s Spec) FrameIntervalNS() int64 {
	return 1_000_000_000 / s.FPS
}

// RenderPixels builds deterministic grayscale pixels for frame seq. Content
// depends only on seq modulo the repeat period so duplicate frames match.
func RenderPixels(seq uint64, w, h uint32) []byte {
	period := uint64(w) * uint64(h)
	pixels := make([]byte, int(w)*int(h))
	for i := range pixels {
		pixels[i] = byte((uint64(i) + seq) % period)
	}
	return pixels
}

// CaptureTime computes the (possibly jumped) capture timestamp for index i.
func (s Spec) CaptureTime(i int) int64 {
	base := int64(i) * s.FrameIntervalNS()
	if s.JumpAt > 0 && i >= s.JumpAt {
		base += s.JumpExtraNS
	}
	if s.DupJumpAt > 0 && i >= s.DupJumpAt {
		base += s.DupJumpExtraNS
	}
	return base
}

// pixelSeq maps a generated frame index to the sequence whose pixels it
// shares, implementing the duplicate-content block.
func (s Spec) pixelSeq(i int) uint64 {
	if s.DuplicateAfter > 0 && i >= s.DuplicateAfter {
		return uint64(i - s.DuplicateAfter)
	}
	return uint64(i)
}

// PixelSeqForTest exposes the content source sequence for a frame index so
// the engine can rebuild the exact reference pixels during verification.
func (s Spec) PixelSeqForTest(i int) uint64 { return s.pixelSeq(i) }

// BuildFrame returns one frame record for index i (not yet serialized).
func (s Spec) BuildFrame(i int) *frame.Record {
	seq := uint64(i)
	pixels := RenderPixels(s.pixelSeq(i), s.Width, s.Height)
	return &frame.Record{
		Version: frame.Version1, Track: "video/0", Seq: seq,
		CaptureNS: s.CaptureTime(i), Width: s.Width, Height: s.Height,
		Color:   frame.ColorMeta{ColorSpace: 2, Primaries: 1, Transfer: 1, FullRange: 1},
		Payload: pixels,
	}
}

// WriteFrameStream serializes all frame records with uint32 length prefixes.
func (s Spec) WriteFrameStream() ([]byte, error) {
	var out bytes.Buffer
	var lenBuf [4]byte
	for i := 0; i < s.Frames; i++ {
		raw, err := frame.MarshalRecord(s.BuildFrame(i))
		if err != nil {
			return nil, err
		}
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
		out.Write(lenBuf[:])
		out.Write(raw)
	}
	return out.Bytes(), nil
}

// Audio constants: 48 kHz clock, 10 ms (480 sample) packets, 4 per frame.
const (
	AudioClockRate = 48000
	SamplesPerPkt  = 480
	PktsPerFrame   = 4
)

// WriteRTPStream generates one RTP packet per 10 ms audio slot. Packets in
// the configured silence range carry zero PCM; the rest carry loud samples.
// The returned stream is length-prefixed the same way as the frame stream.
func (s Spec) WriteRTPStream() ([]byte, error) {
	var out bytes.Buffer
	var lenBuf [4]byte
	totalPkts := s.Frames * PktsPerFrame
	for k := 0; k < totalPkts; k++ {
		frameIndex := k / PktsPerFrame
		silent := frameIndex >= s.SilenceStart && frameIndex <= s.SilenceEnd
		payload := make([]byte, SamplesPerPkt*2)
		if !silent {
			for i := 0; i < len(payload); i += 2 {
				payload[i] = 0x7F
				payload[i+1] = 0xFF
			}
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				SequenceNumber: uint16(k),
				SSRC:           7,
				PayloadType:    111,
				Timestamp:      uint32(k) * SamplesPerPkt,
			},
			Payload: payload,
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, err
		}
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
		out.Write(lenBuf[:])
		out.Write(raw)
	}
	return out.Bytes(), nil
}

// DurationNS is the media duration covered by the generated frames: the span
// from the first frame instant to the last frame instant, i.e. N frame
// intervals for N frames. Bitrate computed against this span matches the
// designed per-frame payload rate exactly.
func (s Spec) DurationNS() int64 {
	return int64(s.Frames) * s.FrameIntervalNS()
}

// LastFrameNS is the capture timestamp of the final generated frame.
func (s Spec) LastFrameNS() int64 {
	return s.CaptureTime(s.Frames - 1)
}

// TargetBitrateBps is the designed payload bitrate: fixed pixel bytes per
// frame at the configured frame rate.
func (s Spec) TargetBitrateBps() float64 {
	bytesPerFrame := float64(s.Width) * float64(s.Height)
	return bytesPerFrame * float64(s.FPS) * 8
}

// PerfectPSNRCap is the finite PSNR reported for lossless (identical)
// frames so summary JSON never has to encode +/-Inf.
const PerfectPSNRCap = 100.0

// PSNR computes the peak signal-to-noise ratio in dB between two equally
// sized grayscale pixel buffers. Identical frames yield +Inf.
func PSNR(a, b []byte) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var mse float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		mse += d * d
	}
	mse /= float64(len(a))
	if mse == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10((255*255)/mse)
}

// SSIM computes a global structural similarity index for two grayscale
// buffers using their means, variances and covariance. Identical frames
// return 1.0.
func SSIM(a, b []byte) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	n := float64(len(a))
	var ma, mb float64
	for i := range a {
		ma += float64(a[i])
		mb += float64(b[i])
	}
	ma /= n
	mb /= n
	var va, vb, cov float64
	for i := range a {
		da := float64(a[i]) - ma
		db := float64(b[i]) - mb
		va += da * da
		vb += db * db
		cov += da * db
	}
	va /= n
	vb /= n
	cov /= n
	const c1 = (0.01 * 255) * (0.01 * 255)
	const c2 = (0.03 * 255) * (0.03 * 255)
	num := (2*ma*mb + c1) * (2*cov + c2)
	den := (ma*ma + mb*mb + c1) * (va + vb + c2)
	if den == 0 {
		return 1
	}
	return num / den
}

// BitrateBps converts a payload byte count and nanosecond duration to bps.
func BitrateBps(payloadBytes int64, durationNS int64) float64 {
	if durationNS <= 0 {
		return 0
	}
	seconds := float64(durationNS) / 1_000_000_000
	return float64(payloadBytes) * 8 / seconds
}

// CorruptChecksum flips one payload byte inside a serialized frame stream
// without updating its SHA-256, producing a checksum fault at parse time.
func CorruptChecksum(frameStream []byte) []byte {
	out := append([]byte(nil), frameStream...)
	if len(out) <= 40 {
		return out
	}
	// The first record body starts after its 4-byte length prefix; flip a
	// byte well inside the payload so the trailing digest no longer matches.
	out[40] ^= 0xFF
	return out
}

// WriteOverflowFrameStream builds a small valid stream whose final frame
// carries a capture timestamp at the int64 ceiling, forcing segment-end
// arithmetic to overflow. All earlier frames are valid.
func WriteOverflowFrameStream() ([]byte, error) {
	spec := DefaultSpec()
	var out bytes.Buffer
	var lenBuf [4]byte
	put := func(rec *frame.Record) error {
		raw, err := frame.MarshalRecord(rec)
		if err != nil {
			return err
		}
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
		out.Write(lenBuf[:])
		out.Write(raw)
		return nil
	}
	for i := 0; i < 4; i++ {
		if err := put(spec.BuildFrame(i)); err != nil {
			return nil, err
		}
	}
	bad := spec.BuildFrame(4)
	bad.CaptureNS = math.MaxInt64
	if err := put(bad); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
