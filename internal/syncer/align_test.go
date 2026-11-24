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

// audioPlaced builds recovered 20 ms units at 48 kHz, anchored at baseTick.
func audioPlaced(t *testing.T, n int, baseTick int64) []Placed {
	t.Helper()
	tl := tl90000(t)
	units := make([]recovery.Unit, n)
	for i := range units {
		p := &rtpparse.Packet{Timestamp: uint32(i * 960)}
		units[i] = recovery.Unit{
			Seq:          uint64(i),
			RTPTimestamp: p.Timestamp,
			TickSpan:     960,
			Packet:       p,
		}
	}
	placed, faults := Place(units, tl, Config{ToleranceTicks: 900, AudioClockRate: 48000, BaseTick: baseTick, AudioTrack: 2})
	if faults.Len() != 0 {
		t.Fatalf("place faults: %v", faults)
	}
	return placed
}

func TestAlignSyncedTracks(t *testing.T) {
	tl := tl90000(t)
	cfg := Config{ToleranceTicks: 900, AudioClockRate: 48000, AudioTrack: 2}
	// 25 video frames = 1 s. 50 audio units = 1 s.
	rep, err := Align(videoFrames(0, 25), audioPlaced(t, 50, 0), tl, cfg)
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
	// The sidecar declares audio to begin 50 ms after video (4500 ticks at
	// 90 kHz). That offset is applied, so the drift equals the offset.
	cfg := Config{ToleranceTicks: 450, AudioClockRate: 48000, AudioOffsetTicks: 4500, AudioTrack: 2}
	v := videoFrames(3600, 25)
	placed := audioPlaced(t, 50, 3600+4500)
	rep, err := Align(v, placed, tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Audio[0].LocalTick != 3600+4500 {
		t.Fatalf("audio start = %d", rep.Audio[0].LocalTick)
	}
	if rep.StartDelta != 4500 {
		t.Fatalf("start delta = %d want 4500", rep.StartDelta)
	}
	if rep.Synced {
		t.Fatal("offset beyond tolerance must be flagged")
	}
	if rep.Faults.CountKind(fault.KindDesync) != 1 {
		t.Fatalf("faults = %s", rep.Faults.Summary())
	}

	// The same offset with a matching tolerance is accepted.
	cfg.ToleranceTicks = 4500
	rep, err = Align(v, placed, tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Synced {
		t.Fatalf("offset within tolerance flagged: %v", rep.Faults)
	}
}

func TestAlignDesyncBeyondTolerance(t *testing.T) {
	tl := tl90000(t)
	// Audio starts 7200 ticks late (~80 ms) but tolerance is 900.
	cfg := Config{ToleranceTicks: 900, AudioClockRate: 48000, AudioOffsetTicks: 7200, AudioTrack: 2}
	rep, err := Align(videoFrames(0, 25), audioPlaced(t, 50, 7200), tl, cfg)
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
	tl2 := tl90000(t)
	units := make([]recovery.Unit, 3)
	for i := range units {
		ts := uint32(0xFFFFFC00 + i*960)
		p := &rtpparse.Packet{Timestamp: ts}
		units[i] = recovery.Unit{Seq: uint64(i), RTPTimestamp: ts, TickSpan: 960, Packet: p}
	}
	cfg := Config{ToleranceTicks: 900, AudioClockRate: 48000, AudioTrack: 2, BaseRTP: 0xFFFFFC00}
	placed, pf := Place(units, tl2, cfg)
	if pf.Len() != 0 {
		t.Fatalf("place faults: %v", pf)
	}
	rep, err := Align(videoFrames(0, 3), placed, tl, cfg)
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
	rep, err := Align(recs, audioPlaced(t, 10, 0), tl, cfg)
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

func TestAlignAudioTailOutIsNotDrift(t *testing.T) {
	tl := tl90000(t)
	cfg := Config{ToleranceTicks: 900, AudioClockRate: 48000, AudioTrack: 2}
	// Audio runs 200 ms past the end of the video: a normal tail.
	rep, err := Align(videoFrames(0, 25), audioPlaced(t, 60, 0), tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Synced {
		t.Fatalf("tail-out flagged as desync: %v", rep.Faults)
	}
	if rep.EndDelta <= 0 {
		t.Fatalf("end delta = %d want positive", rep.EndDelta)
	}
}

func TestAlignShortTailWithinOnePacketIsAccepted(t *testing.T) {
	tl := tl90000(t)
	cfg := Config{ToleranceTicks: 100, AudioClockRate: 48000, AudioTrack: 2}
	// Video runs 26 frames (1.04 s = 93600 ticks). 52 audio slots cover it
	// exactly; 51 leave 1800 ticks (one 20 ms slot) silent at the cut, which is
	// inside the one-slot slack and is accepted.
	v := videoFrames(0, 26)
	placed := audioPlaced(t, 51, 0)
	rep, err := Align(v, placed, tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Synced {
		t.Fatalf("one-slot tail flagged: start=%d end=%d", rep.StartDelta, rep.EndDelta)
	}
	// Two slots short crosses the slack and is flagged.
	placed = audioPlaced(t, 50, 0)
	rep, err = Align(v, placed, tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Synced {
		t.Fatalf("two-slot tail accepted: end=%d", rep.EndDelta)
	}
}

func TestAlignEarlyAudioEndIsDesync(t *testing.T) {
	tl := tl90000(t)
	cfg := Config{ToleranceTicks: 100, AudioClockRate: 48000, AudioTrack: 2}
	// Audio stops a full second before the video.
	rep, err := Align(videoFrames(0, 50), audioPlaced(t, 25, 0), tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Synced {
		t.Fatal("truncated audio track not flagged")
	}
	if rep.Faults.CountKind(fault.KindDesync) != 1 {
		t.Fatalf("faults = %s", rep.Faults.Summary())
	}
}

func TestAlignLateAudioStartIsDrift(t *testing.T) {
	tl := tl90000(t)
	cfg := Config{ToleranceTicks: 450, AudioClockRate: 48000, AudioTrack: 2}
	// Audio begins 100 ms after the video.
	rep, err := Align(videoFrames(0, 50), audioPlaced(t, 55, 9000), tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Synced {
		t.Fatal("late audio start not flagged")
	}
	if rep.StartDelta != 9000 {
		t.Fatalf("start delta = %d", rep.StartDelta)
	}
}

func TestPlaceRejectsZeroClockRate(t *testing.T) {
	tl := tl90000(t)
	units := []recovery.Unit{{Seq: 1, RTPTimestamp: 100, TickSpan: 960}}
	placed, faults := Place(units, tl, Config{AudioClockRate: 0})
	if len(placed) != 0 || faults.Len() != 1 {
		t.Fatalf("placed=%d faults=%v", len(placed), faults)
	}
	if faults[0].Kind != fault.KindConfig {
		t.Fatalf("kind = %s", faults[0].Kind)
	}
}
