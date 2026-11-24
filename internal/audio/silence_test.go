package audio

import (
	"testing"

	"mediapipe/internal/model"
)

func makeAudioPacket(t *testing.T, mediaMs int64, amplitude int16, valid bool) model.Packet {
	t.Helper()
	samples := make([]int16, 72)
	for i := range samples {
		samples[i] = amplitude
	}
	p := model.Packet{MediaMs: mediaMs, FrameMs: 20, Payload: EncodePayload(samples, 8000), PayloadValid: true}
	if !valid {
		p.PayloadValid = false
		p.PayloadReason = "crc_mismatch"
	}
	return p
}

func TestPayloadEncodeDecodeAndCorruption(t *testing.T) {
	samples := []int16{0, 1, -1, 32000, -32000}
	raw := EncodePayload(samples, 8000)
	got, rate, err := DecodePayload(raw)
	if err != nil || rate != 8000 {
		t.Fatalf("decode rate=%d err=%v", rate, err)
	}
	if len(got) != len(samples) || got[3] != 32000 || got[4] != -32000 {
		t.Fatalf("samples mismatch: %v", got)
	}
	raw[len(raw)-1] ^= 0xff
	if _, _, err := DecodePayload(raw); err == nil {
		t.Fatal("expected corruption to fail validation")
	}
}

func TestSilenceDetectorClassifiesSilentAndVoiced(t *testing.T) {
	d := NewDetector(20, 300)
	seg := model.Segment{Index: 0, StartMs: 0, EndMs: 60}
	packets := []model.Packet{
		makeAudioPacket(t, 0, 0, true),
		makeAudioPacket(t, 20, 1200, true),
		makeAudioPacket(t, 40, 0, true),
	}
	windows := d.Detect(seg, packets)
	if len(windows) != 3 {
		t.Fatalf("windows=%d", len(windows))
	}
	if windows[0].State != model.WindowSilent {
		t.Fatalf("w0=%s amp=%v", windows[0].State, windows[0].Amplitude)
	}
	if windows[1].State != model.WindowVoiced {
		t.Fatalf("w1=%s amp=%v", windows[1].State, windows[1].Amplitude)
	}
	if windows[2].State != model.WindowSilent {
		t.Fatalf("w2=%s", windows[2].State)
	}
}

func TestCorruptAndMissingWindowsAreUnknownNotSilent(t *testing.T) {
	d := NewDetector(20, 300)
	seg := model.Segment{Index: 2, StartMs: 100, EndMs: 180}
	packets := []model.Packet{
		makeAudioPacket(t, 100, 0, true),
		makeAudioPacket(t, 120, 0, false), // corrupt
		// 140 missing
		makeAudioPacket(t, 160, 0, true),
	}
	windows := d.Detect(seg, packets)
	want := []model.WindowState{model.WindowSilent, model.WindowUnknown, model.WindowUnknown, model.WindowSilent}
	if len(windows) != len(want) {
		t.Fatalf("windows=%d want %d", len(windows), len(want))
	}
	for i, w := range windows {
		if w.State != want[i] {
			t.Fatalf("window %d state=%s want %s reason=%s", i, w.State, want[i], w.Reason)
		}
	}
}
