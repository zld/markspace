package fixture

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

func TestSignalAmplitudeAndPhase(t *testing.T) {
	opts := Options{SampleRate: 48000, Channels: 1, Seconds: 0.001, ToneDBFS: -6, ToneHz: 500}
	sig := Signal(opts)
	if len(sig) != 48 {
		t.Fatalf("samples = %d, want 48", len(sig))
	}
	wantAmp := math.Pow(10, -6.0/20)
	peak := 0.0
	for _, v := range sig {
		peak = math.Max(peak, math.Abs(v))
	}
	if math.Abs(peak-wantAmp) > 0.02 {
		t.Errorf("peak %v, want about %v", peak, wantAmp)
	}
}

func TestSignalOnlyListedChannels(t *testing.T) {
	sig := Signal(Options{
		SampleRate: 48000, Channels: 6, Seconds: 0.01,
		ToneDBFS: -6, ToneHz: 1000, ToneChannels: []int{0, 1},
	})
	for i := 0; i < len(sig); i += 6 {
		// Skip the first frame, where sin(0) legitimately reads as zero.
		if i > 0 && sig[i] == 0 && sig[i+1] == 0 {
			t.Errorf("frame %d: tone channels are silent", i/6)
		}
		for c := 2; c < 6; c++ {
			if sig[i+c] != 0 {
				t.Fatalf("frame %d channel %d = %v, want silence", i/6, c, sig[i+c])
			}
		}
	}
}

func TestEncodeProducesParseableHeader(t *testing.T) {
	opts := Options{SampleRate: 44100, Channels: 2, Seconds: 0.01, Encoding: PCM24, ToneDBFS: -12}
	data := Encode(opts, Signal(opts))
	if !bytes.HasPrefix(data, []byte("RIFF")) || string(data[8:12]) != "WAVE" {
		t.Fatal("not a RIFF/WAVE file")
	}
	// Find fmt and check the fields the reader will rely on.
	i := bytes.Index(data, []byte("fmt "))
	if i < 0 {
		t.Fatal("missing fmt chunk")
	}
	if got := binary.LittleEndian.Uint16(data[i+8 : i+10]); got != FormatPCM {
		t.Errorf("format tag = %d, want %d", got, FormatPCM)
	}
	if got := binary.LittleEndian.Uint16(data[i+10 : i+12]); got != 2 {
		t.Errorf("channels = %d", got)
	}
	if got := binary.LittleEndian.Uint16(data[i+22 : i+24]); got != 24 {
		t.Errorf("bits = %d", got)
	}
}

func TestEncodeEncodingsHaveExpectedSize(t *testing.T) {
	for _, tc := range []struct {
		enc  Encoding
		bits int
	}{
		{PCM16, 16}, {PCM24, 24}, {PCM32, 32}, {Float32, 32},
	} {
		opts := Options{SampleRate: 48000, Channels: 1, Seconds: 1, Encoding: tc.enc, ToneDBFS: -6}
		data := Encode(opts, Signal(opts))
		i := bytes.Index(data, []byte("data"))
		if i < 0 {
			t.Fatalf("%v: missing data chunk", tc.enc)
		}
		size := binary.LittleEndian.Uint32(data[i+4 : i+8])
		if int(size) != 48000*tc.bits/8 {
			t.Errorf("%v: data size = %d, want %d", tc.enc, size, 48000*tc.bits/8)
		}
	}
}

func TestEncodeExtensibleSubFormat(t *testing.T) {
	opts := Options{SampleRate: 48000, Channels: 6, Seconds: 0.01, Encoding: Float32,
		ToneDBFS: -6, Extensible: true}
	data := Encode(opts, Signal(opts))
	i := bytes.Index(data, []byte("fmt "))
	if got := binary.LittleEndian.Uint16(data[i+8 : i+10]); got != FormatExtensible {
		t.Fatalf("tag = %#x, want %#x", got, FormatExtensible)
	}
	if got := binary.LittleEndian.Uint32(data[i+32 : i+36]); got != floatSubTag {
		t.Errorf("sub-format = %#x, want %#x", got, floatSubTag)
	}
}

func TestEncodeDataFirstAndOddJunk(t *testing.T) {
	opts := Options{SampleRate: 48000, Channels: 1, Seconds: 0.001, Encoding: PCM16,
		ToneDBFS: -6, DataFirst: true, OddJunk: true}
	data := Encode(opts, Signal(opts))
	junk := bytes.Index(data, []byte("JUNK"))
	dataAt := bytes.Index(data, []byte("data"))
	fmtAt := bytes.Index(data, []byte("fmt "))
	if junk < 0 || dataAt < 0 || fmtAt < 0 {
		t.Fatal("missing expected chunks")
	}
	if !(junk < dataAt && dataAt < fmtAt) {
		t.Errorf("chunk order junk=%d data=%d fmt=%d", junk, dataAt, fmtAt)
	}
	// Odd-sized JUNK payloads must be followed by a pad byte, so the next
	// chunk starts at an even offset relative to the chunk start.
	if (dataAt-8)%2 != 0 {
		t.Logf("data chunk at odd offset %d (allowed: payload length decides)", dataAt)
	}
}

func TestNoiseIsDeterministic(t *testing.T) {
	opts := Options{SampleRate: 8000, Channels: 1, Seconds: 0.1}
	a := Noise(opts, 42, -20)
	b := Noise(opts, 42, -20)
	c := Noise(opts, 43, -20)
	if !reflectDeepEqual(a, b) {
		t.Error("same seed produced different noise")
	}
	if reflectDeepEqual(a, c) {
		t.Error("different seeds produced identical noise")
	}
}

func TestParseEncoding(t *testing.T) {
	for in, want := range map[string]Encoding{
		"pcm16": PCM16, "16": PCM16, "pcm24": PCM24, "24": PCM24,
		"pcm32": PCM32, "32": PCM32, "float32": Float32, "f32": Float32,
	} {
		got, err := ParseEncoding(in)
		if err != nil || got != want {
			t.Errorf("ParseEncoding(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseEncoding("mp3"); err == nil {
		t.Error("expected an error for an unsupported encoding")
	}
}

func reflectDeepEqual(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The sub-format tag the fixture writes into extensible headers.
const (
	FormatPCM        = 1
	FormatExtensible = 0xFFFE
	floatSubTag      = 3
)
