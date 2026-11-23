package syncer

import (
	"testing"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
	"mediarun/internal/frames"
	"mediarun/internal/recovery"
	rtpparse "mediarun/internal/rtp"
)

func tl90000(t *testing.T) *clock.Timeline {
	t.Helper()
	tl, err := clock.NewTimeline(90000)
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

// videoFrames builds 25 fps frames starting at startTick.
func videoFrames(startTick int64, n int) []*frames.Record {
	out := make([]*frames.Record, n)
	for i := range out {
		out[i] = &frames.Record{
			Offset: int64(i) * 100,
			Index:  i,
			Meta: frames.Metadata{
				Track: 1,
				PTS:   uint64(startTick + int64(i)*3600),
				DTS:   uint64(startTick + int64(i)*3600),
			},
			Payload: []byte("IMG1"),
		}
	}
	return out
}

// audioUnits builds recovered 20 ms units at 48 kHz.
func audioUnits(n int) []recovery.Unit {
	out := make([]recovery.Unit, n)
	for i := range out {
		p := &rtpparse.Packet{Timestamp: uint32(i * 960)}
		out[i] = recovery.Unit{
			Seq:          uint64(i),
			RTPTimestamp: p.Timestamp,
			TickSpan:     960,
			Packet:       p,
		}
	}
	return out
}

func TestAlignSyncedTracks(t *testing.T) {
	tl := tl90000(t)
	cfg := Config{ToleranceTicks: 900, AudioClockRate: 48000, AudioTrack: 2}
	// 25 video frames = 1 s. 50 audio units = 1 s.
	rep, err := Align(videoFrames(0, 25), audioUnits(50), tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Synced {
		t.Fatalf("expected sync, faults: %v", rep.Faults)
	}
	if rep.VideoEnd != 90000 {
		t.Fatalf("video end = %d", rep.VideoEnd)
	}
	if rep.AudioEnd != 90000 {
		t.Fatalf("audio end = %d", rep.AudioEnd)
	}
	if rep.StartDelta != 0 || rep.EndDelta != 0 {
		t.Fatalf("deltas = %d/%d", rep.StartDelta, rep.EndDelta)
	}
	if len(rep.Audio) != 50 {
		t.Fatalf("audio slots = %d", len(rep.Audio))
	}
	if rep.Audio[1].LocalTick != 1800 {
		t.Fatalf("slot tick = %d want 1800", rep.Audio[1].LocalTick)
	}
}

func TestAlignExplicitAudioOffset(t *testing.T) {
	tl := tl90000(t)
	cfg := Config{ToleranceTicks: 450, AudioClockRate: 48000, AudioOffsetTicks: 4500, AudioTrack: 2}
	v := videoFrames(3600, 25)
	rep, err := Align(v, audioUnits(50), tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Synced {
		t.Fatalf("offset should be absorbed, faults %v", rep.Faults)
	}
	if rep.StartDelta != 0 {
		t.Fatalf("start delta = %d", rep.StartDelta)
	}
	if rep.Audio[0].LocalTick != 3600+4500 {
		t.Fatalf("audio start = %d", rep.Audio[0].LocalTick)
	}
}

func TestAlignDesyncBeyondTolerance(t *testing.T) {
	tl := tl90000(t)
	// Audio starts 7200 ticks late (~80 ms) but tolerance is 900.
	cfg := Config{ToleranceTicks: 900, AudioClockRate: 48000, AudioOffsetTicks: 7200, AudioTrack: 2}
	rep, err := Align(videoFrames(0, 25), audioUnits(45), tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Synced {
		t.Fatal("expected desync flag")
	}
	if rep.Faults.CountKind(fault.KindDesync) != 1 {
		t.Fatalf("faults = %s", rep.Faults.Summary())
	}
	if !rep.Faults[0].Recoverable {
		t.Fatal("desync fault should be recoverable")
	}
	if rep.Faults[0].Track != 2 {
		t.Fatalf("track = %d", rep.Faults[0].Track)
	}
}

func TestAlignRTPWrapAndTrackStart(t *testing.T) {
	tl := tl90000(t)
	units := audioUnits(3)
	// Move timestamps close to wrap: base 0xFFFFFC00.
	for i := range units {
		ts := uint32(0xFFFFFC00 + i*960)
		units[i].RTPTimestamp = ts
		units[i].Packet.Timestamp = ts
	}
	cfg := Config{ToleranceTicks: 900, AudioClockRate: 48000, AudioTrack: 2}
	rep, err := Align(videoFrames(0, 3), units, tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Audio) != 3 {
		t.Fatalf("audio slots = %d", len(rep.Audio))
	}
	if rep.Audio[1].LocalTick != 1800 || rep.Audio[2].LocalTick != 3600 {
		t.Fatalf("wrap ticks = %d %d", rep.Audio[1].LocalTick, rep.Audio[2].LocalTick)
	}
}

func TestAlignDetectsRateChange(t *testing.T) {
	tl := tl90000(t)
	recs := videoFrames(0, 10)
	// Switch to 30 fps after frame 5, keeping timestamps monotonic.
	for i := 5; i < len(recs); i++ {
		recs[i].Meta.PTS = uint64(5*3600 + (i-5)*3000)
		recs[i].Meta.DTS = recs[i].Meta.PTS
	}
	cfg := Config{ToleranceTicks: 9000, AudioClockRate: 48000, AudioTrack: 2}
	rep, err := Align(recs, audioUnits(10), tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.RateChanges == 0 {
		t.Fatal("frame rate change not detected")
	}
	found := false
	for _, v := range rep.Video {
		if v.RateChanged {
			found = true
		}
	}
	if !found {
		t.Fatal("no frame marked rate changed")
	}
}

func TestAlignRejectsNonMonotonicPTS(t *testing.T) {
	tl := tl90000(t)
	recs := videoFrames(0, 3)
	recs[2].Meta.PTS = recs[1].Meta.PTS - 1
	cfg := Config{ToleranceTicks: 90000, AudioClockRate: 48000, AudioTrack: 2}
	rep, err := Align(recs, nil, tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Synced {
		t.Fatal("non-monotonic pts should desync")
	}
	if rep.Faults.CountKind(fault.KindTimestamp) != 1 {
		t.Fatalf("faults = %s", rep.Faults.Summary())
	}
}

func TestAlignValidation(t *testing.T) {
	tl := tl90000(t)
	if _, err := Align(videoFrames(0, 1), nil, nil, Config{AudioClockRate: 48000}); err == nil {
		t.Fatal("nil timeline accepted")
	}
	if _, err := Align(videoFrames(0, 1), nil, tl, Config{AudioClockRate: 0}); err == nil {
		t.Fatal("zero clock rate accepted")
	}
	rep, err := Align(nil, nil, tl, Config{AudioClockRate: 48000})
	if err != nil || rep.Synced {
		t.Fatalf("empty video: %v %+v", err, rep)
	}
}
