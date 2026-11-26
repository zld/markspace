package remux_test

import (
	"errors"
	"testing"

	"liveregress/internal/fault"
	"liveregress/internal/fmp4"
)

func TestCodecPolicy(t *testing.T) {
	if err := fmp4.CodecSupport("ipcm", 3, 24, 48000); err != nil {
		t.Fatalf("ipcm should be supported: %v", err)
	}
	if err := fmp4.CodecSupport("mp4a", 2, 16, 48000); err != nil {
		t.Fatalf("mp4a should be supported: %v", err)
	}
	cases := []struct {
		codec string
		ch    int
		size  int
	}{
		{"ipcm", 3, 8},  // unsupported PCM depth
		{"ipcm", 0, 24}, // no channels
		{"enca", 3, 24}, // protected
		{"ac-3", 6, 16}, // not copyable
		{"opus", 2, 16}, // not copyable
	}
	for _, c := range cases {
		err := fmp4.CodecSupport(c.codec, c.ch, c.size, 48000)
		if !errors.Is(err, fault.ErrUnsupported) {
			t.Fatalf("%s: want unsupported, got %v", c.codec, err)
		}
	}
}
