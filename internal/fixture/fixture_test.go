package fixture

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"

	"example.com/mediatimeline/internal/ingest"
	"example.com/mediatimeline/internal/model"
)

func TestBuildScenarioIsDeterministic(t *testing.T) {
	a, err := BuildScenario(DefaultOptions(42))
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildScenario(DefaultOptions(42))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.MP4, b.MP4) {
		t.Fatal("MP4 bytes differ between identical seeds")
	}
	if len(a.VideoRTP) != len(b.VideoRTP) || len(a.AudioRTP) != len(b.AudioRTP) {
		t.Fatal("packet counts differ between identical seeds")
	}
	for i := range a.VideoRTP {
		if !bytes.Equal(a.VideoRTP[i], b.VideoRTP[i]) {
			t.Fatalf("video packet %d differs", i)
		}
	}
	// A different seed produces different media.
	c, err := BuildScenario(DefaultOptions(43))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.MP4, c.MP4) {
		t.Fatal("different seeds produced identical MP4")
	}
}

func TestBuildMP4ProbesAndReads(t *testing.T) {
	sc, err := BuildScenario(DefaultOptions(7))
	if err != nil {
		t.Fatal(err)
	}
	info, err := mp4.Probe(bytes.NewReader(sc.MP4))
	if err != nil {
		t.Fatalf("go-mp4 cannot probe the fixture: %v", err)
	}
	if len(info.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(info.Tracks))
	}
	v, err := ingest.ReadMP4(sc.MP4, ingest.MP4Options{Track: model.TrackVideoMain, Kind: model.KindVideo})
	if err != nil {
		t.Fatalf("video read: %v", err)
	}
	if len(v.Samples) != sc.Reference.VideoSamples {
		t.Fatalf("video samples = %d, want %d", len(v.Samples), sc.Reference.VideoSamples)
	}
	if v.Color == nil || v.Color.Primaries != 1 {
		t.Fatalf("video color = %+v, want the BT.709 container declaration", v.Color)
	}
	// Exactly one in three samples is a sync sample for the 30 fps fixture.
	syncs := 0
	for _, s := range v.Samples {
		if s.Sync {
			syncs++
		}
	}
	if syncs != len(sc.Reference.KeyframeTicks) {
		t.Fatalf("sync samples = %d, want %d", syncs, len(sc.Reference.KeyframeTicks))
	}
	a, err := ingest.ReadMP4(sc.MP4, ingest.MP4Options{Track: model.TrackAudioMain, Kind: model.KindAudio})
	if err != nil {
		t.Fatalf("audio read: %v", err)
	}
	if len(a.Samples) != sc.Reference.AudioSamples {
		t.Fatalf("audio samples = %d, want %d", len(a.Samples), sc.Reference.AudioSamples)
	}
	if a.SampleRate != 48000 || a.ChannelCount != 2 {
		t.Fatalf("audio descriptor = %d Hz x %d channels", a.SampleRate, a.ChannelCount)
	}
}

func TestFixtureColorConflictIsReal(t *testing.T) {
	opts := DefaultOptions(42)
	opts.Faults[FaultColorConflict] = true
	sc, err := BuildScenario(opts)
	if err != nil {
		t.Fatal(err)
	}
	v, err := ingest.ReadMP4(sc.MP4, ingest.MP4Options{Track: model.TrackVideoMain, Kind: model.KindVideo})
	if err != nil {
		t.Fatal(err)
	}
	if v.Color == nil || v.Color.Primaries != 1 {
		t.Fatal("the container must still declare BT.709")
	}
	// The bitstream SEI must carry BT.2020 so the conflict is observable.
	// Frames 0 and every tenth frame after carry a SEI message.
	au, ok := v.SamplePayload(10)
	if !ok {
		t.Fatal("no sample payload")
	}
	msgs, _ := ingest.ParseSEI(au)
	if len(msgs) == 0 {
		t.Fatal("fixture access unit carries no SEI")
	}
	cols := ingest.SEIColors(msgs)
	if len(cols) != 1 || cols[0].Primaries != 9 || cols[0].Transfer != 14 || cols[0].Matrix != 9 {
		t.Fatalf("bitstream color = %+v, want BT.2020", cols)
	}
	if cols[0].Source != model.ColorFromBitstream {
		t.Fatalf("source = %s, want sei-colour", cols[0].Source)
	}
}

func TestBuildScenarioRejectsBadOptions(t *testing.T) {
	cases := []ScenarioOptions{
		{Seed: 1, Ladder: 0, SegmentMS: 2000, Frames: 10, FPSNum: 30, FPSDen: 1},
		{Seed: 1, Ladder: 9, SegmentMS: 2000, Frames: 10, FPSNum: 30, FPSDen: 1},
		{Seed: 1, Ladder: 3, SegmentMS: 0, Frames: 10, FPSNum: 30, FPSDen: 1},
		{Seed: 1, Ladder: 3, SegmentMS: 2000, Frames: 0, FPSNum: 30, FPSDen: 1},
	}
	for i, c := range cases {
		if _, err := BuildScenario(c); err == nil {
			t.Fatalf("case %d: expected an error", i)
		}
	}
}
