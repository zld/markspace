package audio

import "testing"

func TestOpusTOCDuration20ms(t *testing.T) {
	// config 1 (SILK NB 20ms) => 960 samples at 48 kHz, one frame.
	info, err := ParseOpusTOC([]byte{0x08, 0xde, 0xad})
	if err != nil {
		t.Fatal(err)
	}
	if info.FrameSamples != 960 || info.FrameCount != 1 || info.Duration48k != 960 {
		t.Fatalf("info=%+v want 960/1/960", info)
	}
	p := Params{Codec: CodecOpus, SampleRate: 48000}
	d, err := PacketDuration(p, []byte{0x08, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	if d.Samples != 960 || d.Num != 960 || d.Den != 48000 {
		t.Fatalf("duration=%+v", d)
	}
}

func TestOpusStereoCode3FrameCount(t *testing.T) {
	// code 3 signals multiple frames; frame count byte bottom 6 bits = 3.
	info, err := ParseOpusTOC([]byte{0x0b, 0x03}) // config1, stereo, code3
	if err != nil {
		t.Fatal(err)
	}
	if info.FrameCount != 3 || info.Duration48k != 2880 {
		t.Fatalf("info=%+v want 3 frames / 2880", info)
	}
}

func TestAACRawUsesConfiguredFrame(t *testing.T) {
	p := Params{Codec: CodecAAC, SampleRate: 44100,
		AAC: AACConfig{AUHeaderBits: 0, SamplesPerFrame: 1024, SampleRate: 44100}}
	d, err := PacketDuration(p, make([]byte, 20))
	if err != nil {
		t.Fatal(err)
	}
	if d.Samples != 1024 || d.Den != 44100 {
		t.Fatalf("duration=%+v", d)
	}
}

func TestAACAUHeaderDrivesDuration(t *testing.T) {
	// One AU: AU-headers-length=16 bits, then a 13-bit size of 8 plus flags.
	payload := []byte{
		0x00, 0x10, // AU-headers-length = 16
		0x00, 0x40, // AU size 8 ((0x00<<5)|(0x40>>3)), low flag bits zero
		0, 0, 0, 0, 0, 0, 0, 0, // 8 bytes of AAC payload
	}
	cfg := AACConfig{AUHeaderBits: 16, SamplesPerFrame: 1024, SampleRate: 48000}
	samples, err := aacDuration(cfg, payload)
	if err != nil {
		t.Fatal(err)
	}
	if samples != 1024 {
		t.Fatalf("samples=%d want 1024", samples)
	}
}

func TestAACAUHeaderRejectsSizePastPayload(t *testing.T) {
	payload := []byte{
		0x00, 0x10,
		0xff, 0xf8, // declares a huge AU size
		0x00,
	}
	cfg := AACConfig{AUHeaderBits: 16, SamplesPerFrame: 1024, SampleRate: 48000}
	if _, err := aacDuration(cfg, payload); err == nil {
		t.Fatal("expected out-of-range AU size error")
	}
}

func TestParamsValidateRejectsUnknownCodec(t *testing.T) {
	if err := (Params{Codec: "mp3", SampleRate: 48000}).Validate(); err == nil {
		t.Fatal("expected unsupported codec error")
	}
	if _, err := ParseCodec("aac"); err != nil {
		t.Fatalf("aac should be supported: %v", err)
	}
}
