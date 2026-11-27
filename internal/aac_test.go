package rtpreplay

import "testing"

func TestParseAudioSpecificConfigLC(t *testing.T) {
	// AOT=2 (LC), freqIndex=3 (48kHz), channelConfig=2.
	// bits: 00010 0011 0010 0 -> 0x11 0x90.
	asc := []byte{0b0001_0001, 0b1001_0000}
	cfg, err := ParseAudioSpecificConfig(asc)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AudioObjectType != 2 || cfg.SampleRate != 48000 || cfg.ChannelConfig != 2 {
		t.Fatalf("cfg=%+v", cfg)
	}
	if cfg.SamplesPerFrame() != 1024 {
		t.Fatalf("samples=%d", cfg.SamplesPerFrame())
	}
}

func TestParseAudioSpecificConfigHEAAC(t *testing.T) {
	// AOT=5, freqIndex=6 (24kHz), channelConfig=2, extFreqIndex=3, coreAOT=2.
	w := newTestBitWriter()
	w.write(5, 5) // AOT SBR
	w.write(6, 4) // 24 kHz
	w.write(2, 4) // stereo
	w.write(3, 4) // extension sampling index 48 kHz
	w.write(2, 5) // core AAC-LC
	w.write(0, 1)
	cfg, err := ParseAudioSpecificConfig(w.bytes())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AudioObjectType != 5 || cfg.SampleRate != 24000 || cfg.ChannelConfig != 2 {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestParseAudioSpecificConfigExplicitRate(t *testing.T) {
	w := newTestBitWriter()
	w.write(2, 5)
	w.write(15, 4)
	w.write(12345, 24)
	w.write(1, 4)
	w.write(1, 1)
	cfg, err := ParseAudioSpecificConfig(w.bytes())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SampleRate != 12345 || !cfg.FrameLengthFlag || cfg.SamplesPerFrame() != 960 {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestParseAudioSpecificConfigReservedAOT(t *testing.T) {
	w := newTestBitWriter()
	w.write(0, 5)
	w.write(3, 4)
	w.write(2, 4)
	if _, err := ParseAudioSpecificConfig(w.bytes()); err == nil {
		t.Fatal("expected reserved AOT error")
	}
	w2 := newTestBitWriter()
	w2.write(2, 5)
	w2.write(13, 4) // reserved frequency index
	w2.write(2, 4)
	if _, err := ParseAudioSpecificConfig(w2.bytes()); err == nil {
		t.Fatal("expected reserved frequency error")
	}
}

func TestParseAudioSpecificConfigTruncated(t *testing.T) {
	if _, err := ParseAudioSpecificConfig([]byte{0x12}); err == nil {
		t.Fatal("expected truncation error")
	}
}

func TestParseLATMHeaderFrameLength(t *testing.T) {
	w := newTestBitWriter()
	w.write(0, 1) // LATM not LOAS
	w.write(1, 1) // useSameStream
	w.write(0, 1) // useSameTime = 0
	w.write(0, 3) // frameLengthType 0
	w.write(200, 8)
	h, err := ParseLATMHeader(w.bytes())
	if err != nil {
		t.Fatal(err)
	}
	if h.Delta != 200 || h.UseSameTime || h.FrameLengthType != 0 {
		t.Fatalf("h=%+v", h)
	}
}

func TestParseLATMHeaderSameTime(t *testing.T) {
	w := newTestBitWriter()
	w.write(0, 1)
	w.write(1, 1)
	w.write(1, 1)
	h, err := ParseLATMHeader(w.bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !h.UseSameTime || h.Delta != 0 {
		t.Fatalf("h=%+v", h)
	}
}

func TestParseLATMHeaderExternalFrameLength(t *testing.T) {
	w := newTestBitWriter()
	w.write(0, 1)
	w.write(1, 1)
	w.write(0, 1)
	w.write(7, 3)
	if _, err := ParseLATMHeader(w.bytes()); err == nil {
		t.Fatal("expected unsupported frameLengthType error")
	}
}

func TestMP4TrackTotalTicksOverflow(t *testing.T) {
	tr := MP4Track{Samples: []MP4Sample{{Duration: ^uint32(0)}, {Duration: ^uint32(0)}}}
	tr.Samples = append(tr.Samples, MP4Sample{Duration: 1})
	total, err := tr.TotalTicks()
	if err != nil {
		t.Fatal(err)
	}
	if total != 2*uint64(^uint32(0))+1 {
		t.Fatalf("total=%d", total)
	}
}
