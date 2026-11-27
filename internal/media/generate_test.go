package media

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"example.com/continuity/internal/frame"
)

func TestFrameStreamRoundTrip(t *testing.T) {
	spec := DefaultSpec()
	data, err := spec.WriteFrameStream()
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(data)
	count := 0
	for {
		var lenBuf [4]byte
		if _, err := r.Read(lenBuf[:]); err != nil {
			break
		}
		n := binary.BigEndian.Uint32(lenBuf[:])
		body := make([]byte, n)
		if _, err := r.Read(body); err != nil {
			t.Fatal(err)
		}
		rec, err := frame.ParseRecord(body, 0, 0)
		if err != nil {
			t.Fatalf("frame %d: %v", count, err)
		}
		if rec.Width != spec.Width || rec.Height != spec.Height {
			t.Fatalf("dims mismatch frame %d", count)
		}
		count++
	}
	if count != spec.Frames {
		t.Fatalf("frames=%d want %d", count, spec.Frames)
	}
}

func TestDuplicateContent(t *testing.T) {
	spec := DefaultSpec()
	// Frame 96..119 repeat content of 0..23.
	a := RenderPixels(spec.pixelSeq(96), spec.Width, spec.Height)
	b := RenderPixels(spec.pixelSeq(0), spec.Width, spec.Height)
	if !bytes.Equal(a, b) {
		t.Fatal("duplicate block content differs")
	}
	c := RenderPixels(spec.pixelSeq(50), spec.Width, spec.Height)
	if bytes.Equal(a, c) {
		t.Fatal("distinct frames collided")
	}
}

func TestPSNRAndSSIM(t *testing.T) {
	a := RenderPixels(3, 8, 8)
	if ps := PSNR(a, a); !math.IsInf(ps, 1) {
		t.Fatalf("identical PSNR = %v, want +Inf", ps)
	}
	if s := SSIM(a, a); s != 1.0 {
		t.Fatalf("identical SSIM = %v, want 1", s)
	}
	noisy := append([]byte(nil), a...)
	for i := range noisy {
		noisy[i] = a[i] ^ 0x01
	}
	ps := PSNR(a, noisy)
	if ps < 40 {
		t.Fatalf("PSNR too low for 1-LSB noise: %v", ps)
	}
	s := SSIM(a, noisy)
	if s < 0.98 {
		t.Fatalf("SSIM too low for near-identical frames: %v", s)
	}
	if PSNR(a, []byte{1}) != 0 || SSIM(a, []byte{1}) != 0 {
		t.Fatal("mismatched lengths must yield zero")
	}
}

func TestBitrateMatchesTarget(t *testing.T) {
	spec := DefaultSpec()
	payloadBytes := int64(spec.Frames) * int64(spec.Width) * int64(spec.Height)
	actual := BitrateBps(payloadBytes, spec.DurationNS())
	target := spec.TargetBitrateBps()
	errPct := math.Abs(actual-target) / target * 100
	if errPct > 5.0 {
		t.Fatalf("bitrate target=%v actual=%v err=%.3f%%", target, actual, errPct)
	}
}

func TestRTPStreamSilence(t *testing.T) {
	spec := DefaultSpec()
	data, err := spec.WriteRTPStream()
	if err != nil {
		t.Fatal(err)
	}
	// Count packets and confirm a silence-range packet is all-zero PCM
	// while an out-of-range packet is not.
	r := bytes.NewReader(data)
	var silentPkt, loudPkt []byte
	idx := 0
	for {
		var lenBuf [4]byte
		if _, err := r.Read(lenBuf[:]); err != nil {
			break
		}
		n := binary.BigEndian.Uint32(lenBuf[:])
		body := make([]byte, n)
		if _, err := r.Read(body); err != nil {
			t.Fatal(err)
		}
		// RTP header is 12 bytes; PCM payload follows.
		payload := body[12:]
		frameIndex := idx / PktsPerFrame
		if frameIndex == spec.SilenceStart {
			silentPkt = payload
		}
		if frameIndex == 0 {
			loudPkt = payload
		}
		idx++
	}
	if idx != spec.Frames*PktsPerFrame {
		t.Fatalf("rtp packets=%d", idx)
	}
	for _, b := range silentPkt {
		if b != 0 {
			t.Fatal("silence packet not zero PCM")
		}
	}
	if bytes.Equal(loudPkt, make([]byte, len(loudPkt))) {
		t.Fatal("loud packet rendered silent")
	}
}

func TestCaptureTimeJump(t *testing.T) {
	spec := DefaultSpec()
	before := spec.CaptureTime(spec.JumpAt - 1)
	at := spec.CaptureTime(spec.JumpAt)
	if at-before != spec.FrameIntervalNS()+spec.JumpExtraNS {
		t.Fatalf("jump gap = %d", at-before)
	}
}
