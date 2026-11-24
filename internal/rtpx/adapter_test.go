package rtpx_test

import (
	"testing"

	"media-archiver/internal/media"
	"media-archiver/internal/rtpx"
)

func TestRTPFrameTimestampExtensionAdapter(t *testing.T) {
	raw, err := rtpx.PacketWithFrameExtension(48000, 99, 3, 1440)
	if err != nil {
		t.Fatal(err)
	}
	adapter := rtpx.Adapter{Timebase: media.Timebase{SampleRate: 48000}, ExtensionID: 3}
	ext, err := adapter.ParsePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if ext.ExtendedFrame != 1440 {
		t.Fatalf("frame=%d", ext.ExtendedFrame)
	}
	if ext.TimeNS != 30_000_000 {
		t.Fatalf("time=%d", ext.TimeNS)
	}
	if ext.RTPTimestamp != 99 {
		t.Fatalf("rtp timestamp=%d", ext.RTPTimestamp)
	}
}

func TestRTPAdapterRejectsMissingExtension(t *testing.T) {
	raw, err := rtpx.PacketWithFrameExtension(48000, 1, 3, 10)
	if err != nil {
		t.Fatal(err)
	}
	adapter := rtpx.Adapter{Timebase: media.Timebase{SampleRate: 48000}, ExtensionID: 7}
	if _, err := adapter.ParsePacket(raw); err == nil {
		t.Fatal("expected missing extension parse failure")
	}
}
