package media_test

import (
	"testing"

	mp4 "github.com/abema/go-mp4"
	"github.com/pion/rtp"

	"assetcheck/internal/media"
)

// TestExternalClockConversions independently checks the shared rational clock
// against the timescales represented by go-mp4 and pion/rtp types.
func TestExternalClockConversions(t *testing.T) {
	mvhd := &mp4.Mvhd{Timescale: 1000, DurationV0: 1000}
	if got := mvhd.GetType(); got != mp4.BoxTypeMvhd() {
		t.Fatalf("mvhd box type = %q, want mvhd", got)
	}
	mp4TickBase, err := media.Rat(1, int64(mvhd.Timescale))
	if err != nil {
		t.Fatal(err)
	}
	mp4Ticks, err := media.ToTicks(media.Second, mp4TickBase, 1, 7, 42)
	if err != nil {
		t.Fatal(err)
	}
	if uint32(mp4Ticks) != mvhd.DurationV0 {
		t.Fatalf("MP4 ticks = %d, want %d", mp4Ticks, mvhd.DurationV0)
	}

	packet := &rtp.Packet{Header: rtp.Header{Version: 2, Timestamp: 90_000}}
	rtpTicks, err := media.ToTicks(media.Second, media.Timebase90k, 2, 3, 84)
	if err != nil {
		t.Fatal(err)
	}
	if uint32(rtpTicks) != packet.Timestamp {
		t.Fatalf("RTP ticks = %d, want %d", rtpTicks, packet.Timestamp)
	}
}

func TestToTicksOverflowIsLocated(t *testing.T) {
	huge := media.Rational{N: 1 << 62, D: 1}
	_, err := media.ToTicks(huge, media.Timebase48k, 9, 11, 123)
	if err == nil {
		t.Fatal("expected overflow error")
	}
	me, ok := media.AsError(err)
	if !ok {
		t.Fatalf("error %T is not structured: %v", err, err)
	}
	if me.TrackID != 9 || me.BlockIndex != 11 || me.Offset != 123 {
		t.Fatalf("location = track %d block %d offset %d", me.TrackID, me.BlockIndex, me.Offset)
	}
}
