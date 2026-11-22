package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"avcheck/internal/media"
)

func testConfig() GenConfig {
	return GenConfig{
		Seed: 7, Frames: 60, Tracks: 2, Width: 16, Height: 8,
		StartFrameSeq: 1000, StartTicks: 5000, TrackTickSkew: 40,
		TicksPerFrame: 3000, KeyInterval: 10,
	}
}

func TestGenerateDeterministic(t *testing.T) {
	a, err := Generate(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Generate(testConfig())
	if len(a) != 2 || len(a[0]) != 60 {
		t.Fatalf("shape %d x %d", len(a), len(a[0]))
	}
	if a[1][3].Sum != b[1][3].Sum || a[0][0].Seq != 1000 || a[0][0].Ticks != a[0][0].Ticks {
		t.Fatal("generation not deterministic")
	}
	if a[0][0].Ticks == a[1][0].Ticks {
		t.Fatal("tracks must have different start ticks")
	}
}

func TestValidatorRejects(t *testing.T) {
	tracks, _ := Generate(testConfig())
	v := NewValidator(PolicySkip)
	good := tracks[0][5]
	if err := v.Check(&good); err != nil {
		t.Fatalf("clean frame rejected: %v", err)
	}
	// truncated payload
	tr := tracks[0][6]
	tr.Payload = tr.Payload[:len(tr.Payload)-3]
	err := v.Check(&tr)
	me, ok := err.(*media.Error)
	if !ok || me.Kind != media.KindTruncated || me.Track != 1 || me.Seq != 1006 {
		t.Fatalf("truncated: %v", err)
	}
	// checksum corruption
	co := tracks[1][7]
	co.Payload[0] ^= 0xFF
	if err := v.Check(&co); err == nil || err.(*media.Error).Kind != media.KindChecksum {
		t.Fatalf("checksum: %v", err)
	}
	// bad metadata version
	bv := tracks[1][8]
	bv.Meta.Version = 99
	if err := v.Check(&bv); err == nil || err.(*media.Error).Kind != media.KindCorrupt {
		t.Fatalf("version: %v", err)
	}
	if v.Rejected != 3 {
		t.Fatalf("rejected=%d", v.Rejected)
	}
}

func TestValidatorFailTrackPolicy(t *testing.T) {
	tracks, _ := Generate(testConfig())
	v := NewValidator(PolicyFailTrack)
	bad := tracks[0][0]
	bad.Payload = bad.Payload[:1]
	if err := v.Check(&bad); err == nil {
		t.Fatal("expected rejection")
	}
	if !v.FailedTracks[1] {
		t.Fatal("track 1 must be marked failed")
	}
	later := tracks[0][1]
	if err := v.Check(&later); err == nil || err.(*media.Error).Kind != media.KindState {
		t.Fatalf("failed track must reject later frames: %v", err)
	}
}

func writeRead(t *testing.T, frames []media.Frame) *SampleTable {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.mp4")
	if err := WriteMP4(p, 90000, frames); err != nil {
		t.Fatalf("write: %v", err)
	}
	st, err := ReadSampleTable(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return st
}

func TestMP4RoundTrip(t *testing.T) {
	tracks, _ := Generate(testConfig())
	frames := tracks[0]
	st := writeRead(t, frames)
	if st.Timescale != 90000 || len(st.Samples) != 60 {
		t.Fatalf("table: ts=%d n=%d", st.Timescale, len(st.Samples))
	}
	clock, _ := media.NewClock(90000)
	times, err := st.Times(clock)
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range frames {
		want, _ := clock.ToSeconds(f.Ticks - frames[0].Ticks)
		if times[i].Cmp(want) != 0 {
			t.Fatalf("sample %d time %v want %v", i, times[i], want)
		}
		if st.Samples[i].Key != f.Key {
			t.Fatalf("sample %d key mismatch", i)
		}
	}
	ki, err := st.KeyIndex(clock)
	if err != nil {
		t.Fatal(err)
	}
	// every frame time must locate a keyframe (first frame is a keyframe)
	for _, tm := range times {
		if _, ok := ki.Locate(tm); !ok {
			t.Fatalf("no keyframe for %v", tm)
		}
	}
	if ki.HitRate() != 1.0 {
		t.Fatalf("hit rate %v", ki.HitRate())
	}
}

func TestMP4RejectsRegression(t *testing.T) {
	tracks, _ := Generate(testConfig())
	frames := tracks[0]
	frames[10].Ticks = frames[9].Ticks // regression
	p := filepath.Join(t.TempDir(), "bad.mp4")
	err := WriteMP4(p, 90000, frames)
	if err == nil || err.(*media.Error).Kind != media.KindRange {
		t.Fatalf("expected range error, got %v", err)
	}
}

func TestKeyIndexBounds(t *testing.T) {
	clock, _ := media.NewClock(90000)
	mk := func(ticks int64) media.Rat { r, _ := clock.ToSeconds(ticks); return r }
	times := []media.Rat{mk(0), mk(3000), mk(6000)}
	if _, err := NewKeyIndex(times, []int64{0, 1, 60}, 60); err == nil {
		t.Fatal("offset 60 outside 60 samples must be rejected")
	}
	if _, err := NewKeyIndex(times, []int64{0, 1, -1}, 60); err == nil {
		t.Fatal("negative offset must be rejected")
	}
	if _, err := NewKeyIndex(times, []int64{0, 1, 1}, 60); err == nil {
		t.Fatal("non-increasing offsets must be rejected")
	}
	ki, err := NewKeyIndex(times, []int64{0, 10, 20}, 60)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ki.Locate(mk(-1)); ok {
		t.Fatal("lookup before first keyframe must miss")
	}
	if ki.Hits != 0 || ki.Misses != 1 {
		t.Fatalf("stats hits=%d misses=%d", ki.Hits, ki.Misses)
	}
}

func TestReadTruncatedMP4(t *testing.T) {
	tracks, _ := Generate(testConfig())
	p := filepath.Join(t.TempDir(), "t.mp4")
	if err := WriteMP4(p, 90000, tracks[0]); err != nil {
		t.Fatal(err)
	}
	// truncate the file: sample offsets must fall outside the file
	tr := p + ".trunc"
	data := readFile(t, p)
	if err := writeFile(tr, data[:len(data)/2]); err != nil {
		t.Fatal(err)
	}
	_, err := ReadSampleTable(tr)
	if err == nil || err.(*media.Error).Kind != media.KindRange {
		t.Fatalf("expected out-of-range sample table error, got %v", err)
	}
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(p string, b []byte) error { return os.WriteFile(p, b, 0o644) }
