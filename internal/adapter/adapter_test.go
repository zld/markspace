package adapter_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/adapter"
	"mediad/internal/fixture"
	"mediad/internal/media"
)

type badVersionParser struct{ adapter.MP4Parser }

func (badVersionParser) Name() string    { return "mp4-ng" }
func (badVersionParser) Version() string { return "v99" }

func TestRegistryRejectsVersionMismatch(t *testing.T) {
	_, err := adapter.NewRegistry(badVersionParser{})
	if err == nil {
		t.Fatal("expected version mismatch error")
	}
	if !media.IsUnrecoverable(err) {
		t.Fatalf("version mismatch must be unrecoverable, got %v", err)
	}
	var me *media.Error
	if !media.AsPhase(err, &me, media.PhaseProbe) {
		t.Fatalf("error must carry probe phase, got %v", err)
	}
}

func TestRegistryUnknownContainer(t *testing.T) {
	reg, err := adapter.NewRegistry(adapter.MP4Parser{}, adapter.RTPParser{})
	if err != nil {
		t.Fatal(err)
	}
	blob := bytes.NewReader([]byte("this is neither mp4 nor rtpcap, just noise........"))
	_, err = reg.Parse("mystery.bin", blob)
	if err == nil {
		t.Fatal("expected unknown container error")
	}
	var me *media.Error
	if !media.AsPhase(err, &me, media.PhaseProbe) {
		t.Fatalf("want probe phase error, got %v", err)
	}
}

func genFixture(t *testing.T, corrupt string) *fixture.Layout {
	t.Helper()
	l, err := fixture.Generate(fixture.Options{
		Seed: 42, Segments: 8, Corrupt: corrupt, Dir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func parseFile(t *testing.T, reg *adapter.Registry, path string) *adapter.Stream {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := reg.Parse(filepath.Base(path), f)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestMP4ParseTracksEventsEdits(t *testing.T) {
	l := genFixture(t, "")
	reg, _ := adapter.NewRegistry(adapter.MP4Parser{}, adapter.RTPParser{})
	st := parseFile(t, reg, l.Inputs[0])
	if len(st.Tracks) != 3 {
		t.Fatalf("tracks = %d, want 3", len(st.Tracks))
	}
	video := st.Tracks[0]
	if video.Type != media.TrackVideo || video.Codec != "avc1" {
		t.Fatalf("video track: type=%s codec=%q", video.Type, video.Codec)
	}
	if !video.HasKeyframeTable {
		t.Fatal("video track must expose its stss keyframe table")
	}
	if len(video.Events) != l.VideoSamples {
		t.Fatalf("video events = %d, want %d", len(video.Events), l.VideoSamples)
	}
	if len(video.CodecParams) == 0 {
		t.Fatal("codec params must be kept as raw bytes")
	}
	// VFR: not all durations equal.
	d0 := video.Events[0].Duration
	vfr := false
	for _, ev := range video.Events {
		if ev.Duration != d0 {
			vfr = true
			break
		}
	}
	if !vfr {
		t.Fatal("fixture must be VFR")
	}
	// Audio track 1 carries the non-zero-start edit list.
	audio := st.Tracks[1]
	if audio.Language != "eng" {
		t.Fatalf("audio language = %q, want eng", audio.Language)
	}
	if len(audio.Edits) != 1 || audio.Edits[0].MediaTime != 1_000_000_000 {
		t.Fatalf("audio edits = %+v, want 1s media start", audio.Edits)
	}
	// Audio track 2 declares no codec parameters.
	if st.Tracks[2].Codec != "" || len(st.Tracks[2].CodecParams) == 0 {
		t.Fatalf("param-less track must keep raw bytes with empty codec, got %+v",
			st.Tracks[2].Codec)
	}
}

func TestMP4TruncatedBoxRecoversWithGaps(t *testing.T) {
	l := genFixture(t, "truncated-box")
	reg, _ := adapter.NewRegistry(adapter.MP4Parser{}, adapter.RTPParser{})
	st := parseFile(t, reg, l.Inputs[0])
	if len(st.Gaps) == 0 {
		t.Fatal("truncated input must produce recoverable gaps")
	}
	damaged := 0
	for _, tr := range st.Tracks {
		for _, ev := range tr.Events {
			if ev.Damaged {
				damaged++
			}
		}
	}
	if damaged == 0 {
		t.Fatal("truncated input must mark damaged samples")
	}
	t.Logf("damaged=%d gaps=%+v", damaged, st.Gaps)
}

func TestRTPWrapReorderDriftAndLoss(t *testing.T) {
	l := genFixture(t, "")
	reg, _ := adapter.NewRegistry(adapter.MP4Parser{}, adapter.RTPParser{})
	st := parseFile(t, reg, l.Inputs[1])
	if len(st.Tracks) != 1 {
		t.Fatalf("rtp tracks = %d, want 1", len(st.Tracks))
	}
	tr := st.Tracks[0]
	// Events are re-sequenced into media order after the wrap.
	for i := 1; i < len(tr.Events); i++ {
		if tr.Events[i].MediaTime < tr.Events[i-1].MediaTime {
			t.Fatalf("events not media-ordered at %d", i)
		}
	}
	// One lost packet -> exactly one recoverable gap.
	if len(st.Gaps) != 1 {
		t.Fatalf("gaps = %+v, want exactly 1", st.Gaps)
	}
	// Drift jump mid-stream -> two fitted segments, ~200ppm then ~400ppm.
	if len(tr.DriftSegments) != 2 {
		t.Fatalf("drift segments = %+v, want 2", tr.DriftSegments)
	}
	if p := tr.DriftSegments[0].SlopePPM; p < 150 || p > 250 {
		t.Fatalf("seg0 ppm = %v, want ~200", p)
	}
	if p := tr.DriftSegments[1].SlopePPM; p < 350 || p > 450 {
		t.Fatalf("seg1 ppm = %v, want ~400", p)
	}
}

func TestMapTracksDeterministic(t *testing.T) {
	l := genFixture(t, "")
	reg, _ := adapter.NewRegistry(adapter.MP4Parser{}, adapter.RTPParser{})
	var streams []*adapter.Stream
	for _, in := range l.Inputs {
		streams = append(streams, parseFile(t, reg, in))
	}
	a, err := adapter.MapTracks(streams...)
	if err != nil {
		t.Fatal(err)
	}
	b, err := adapter.MapTracks(streams...)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 4 {
		t.Fatalf("mapped tracks = %d, want 4", len(a))
	}
	for i := range a {
		if a[i].Label != b[i].Label || a[i].OutputID != b[i].OutputID {
			t.Fatalf("mapping not deterministic: %v vs %v", a, b)
		}
	}
	// Video sorts first; the two eng audio tracks get stable distinct slots.
	if a[0].Track.Type != media.TrackVideo {
		t.Fatalf("first mapped = %s, want video", a[0].Track.Type)
	}
	langs := map[string]int{}
	for _, m := range a {
		if m.Track.Language == "eng" {
			langs[m.Label]++
		}
	}
	if len(langs) != 2 {
		t.Fatalf("duplicate-language tracks must map to distinct labels: %v", langs)
	}
}
