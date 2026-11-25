package wav

import (
	"testing"

	"mediad/media"
)

func TestValidatePCMComplete(t *testing.T) {
	// 16-bit stereo: frame = 4 bytes. 8 frames => 32 payload bytes.
	buf := buildWAVE(32)
	f := ParseChunks(buf)
	res := ValidatePCM(f, f.Faults)
	if len(media.FatalCategories(res.Faults)) != 0 {
		t.Fatalf("clean PCM must validate, got %+v", res.Faults)
	}
	if res.FrameCount != 8 || res.Partial != 0 {
		t.Fatalf("frames=%d partial=%d, want 8/0", res.FrameCount, res.Partial)
	}
	if res.Info.FrameBytes != 4 || res.Info.BlockAlign != 4 {
		t.Fatalf("geometry wrong: %+v", res.Info)
	}
	if res.DataStart != 44 || res.DataEnd != 76 || len(res.Payload) != 32 {
		t.Fatalf("payload range wrong: [%d,%d) len=%d", res.DataStart, res.DataEnd, len(res.Payload))
	}
}

func TestValidatePCMPartialFrame(t *testing.T) {
	// 35 payload bytes => 8 full frames (32) + 3 trailing bytes.
	buf := buildWAVE(35)
	f := ParseChunks(buf)
	res := ValidatePCM(f, f.Faults)
	if res.FrameCount != 8 || res.Partial != 3 {
		t.Fatalf("frames=%d partial=%d, want 8/3", res.FrameCount, res.Partial)
	}
	var pf *media.Fault
	for i := range res.Faults {
		if res.Faults[i].Category == media.CatPartialFrame {
			pf = &res.Faults[i]
		}
	}
	if pf == nil {
		t.Fatalf("partial frame not reported: %+v", res.Faults)
	}
	if pf.ByteStart != 44+32 || pf.ByteEnd != 44+35 {
		t.Fatalf("partial range = [%d,%d), want [76,79)", pf.ByteStart, pf.ByteEnd)
	}
	if !media.QualityFatal(pf.Category) {
		t.Fatal("partial frame must be quality-fatal")
	}
}

func TestValidatePCMBlockAlignMismatch(t *testing.T) {
	buf := buildWAVE(16)
	// Corrupt block align: correct value is 4, claim 8.
	buf[32] = 8
	f := ParseChunks(buf)
	res := ValidatePCM(f, f.Faults)
	var bc *media.Fault
	for i := range res.Faults {
		if res.Faults[i].ByteStart == 32 && res.Faults[i].ByteEnd == 34 {
			bc = &res.Faults[i]
		}
	}
	if bc == nil || bc.Category != media.CatBadContainer {
		t.Fatalf("block align fault wrong: %+v", res.Faults)
	}
}

func TestValidatePCMByteRateMismatch(t *testing.T) {
	buf := buildWAVE(16)
	// Corrupt byte rate field at offset 28.
	buf[28] = 0xFF
	f := ParseChunks(buf)
	res := ValidatePCM(f, f.Faults)
	found := false
	for _, flt := range res.Faults {
		if flt.Category == media.CatBadContainer && flt.ByteStart == 28 && flt.ByteEnd == 32 {
			found = true
		}
	}
	if !found {
		t.Fatalf("byte rate fault missing: %+v", res.Faults)
	}
}

func TestValidatePCMZeroChannels(t *testing.T) {
	buf := buildWAVE(16)
	buf[22] = 0 // channels field at fmt payload offset +2 -> absolute 22
	f := ParseChunks(buf)
	res := ValidatePCM(f, f.Faults)
	found := false
	for _, flt := range res.Faults {
		if flt.Category == media.CatBadContainer && flt.ByteStart == 22 && flt.ByteEnd == 24 {
			found = true
		}
	}
	if !found {
		t.Fatalf("zero-channel fault missing: %+v", res.Faults)
	}
}

func TestValidatePCMUnsupportedFormat(t *testing.T) {
	buf := buildWAVE(16)
	// Format tag at absolute offset 20: write 3 (IEEE float, out of scope).
	buf[20] = 3
	f := ParseChunks(buf)
	res := ValidatePCM(f, f.Faults)
	found := false
	for _, flt := range res.Faults {
		if flt.Category == media.CatBadContainer && flt.ByteStart == 20 && flt.ByteEnd == 22 {
			found = true
		}
	}
	if !found {
		t.Fatalf("unsupported format fault missing: %+v", res.Faults)
	}
}

func TestValidatePCMOutOfBoundsPayloadClamped(t *testing.T) {
	// Data chunk claims 500 bytes while only 100 payload bytes exist; the
	// RIFF container remains internally consistent.
	buf := buildWAVE(100)
	putU32(buf[40:44], 500)
	f := ParseChunks(buf)
	res := ValidatePCM(f, f.Faults)
	// Payload must be clamped to actual data available, never reading past it.
	if res.DataEnd > int64(len(buf)) {
		t.Fatalf("payload end %d exceeds buffer %d", res.DataEnd, len(buf))
	}
	if len(res.Payload) != 100 {
		t.Fatalf("clamped payload len = %d, want 100", len(res.Payload))
	}
	if !media.HasFatal(res.Faults) {
		t.Fatal("out-of-bounds chunk must remain a fatal fault after validation")
	}
}

func TestValidatePCMTimeBase(t *testing.T) {
	buf := buildWAVE(16)
	f := ParseChunks(buf)
	res := ValidatePCM(f, f.Faults)
	tb := res.Info.TimeBase()
	if tb.SampleRate != 48000 || tb.RTPClock != 0 {
		t.Fatalf("time base = %+v, want sample rate 48000 with no RTP clock", tb)
	}
}
