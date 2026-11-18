package audio

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFixtureDeterministic(t *testing.T) {
	dir := t.TempDir()
	p1, p2 := filepath.Join(dir, "a.wav"), filepath.Join(dir, "b.wav")
	opts := FixtureOptions{Layout: "5.1", Seconds: 1, SampleRate: 48000, TargetLoudness: -20}
	if err := WriteFixture(p1, opts); err != nil {
		t.Fatal(err)
	}
	if err := WriteFixture(p2, opts); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(p1)
	b2, _ := os.ReadFile(p2)
	if !bytes.Equal(b1, b2) {
		t.Fatal("fixture output is not deterministic")
	}
}

func TestFixtureParses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.wav")
	opts := FixtureOptions{Layout: "5.1", Seconds: 0.5, SampleRate: 48000, TargetLoudness: -20}
	if err := WriteFixture(p, opts); err != nil {
		t.Fatal(err)
	}
	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	info := r.Info()
	if info.Channels != 6 || info.SampleRate != 48000 || info.Frames != 24000 {
		t.Fatalf("bad info: %+v", info)
	}
	blk, err := r.ReadBlock(48000)
	if err != nil || blk == nil {
		t.Fatalf("read: %v", err)
	}
	var max float64
	for _, v := range blk[0] {
		if a := v; a < 0 {
			a = -a
		} else if a > max {
			max = a
		}
		if a := v; a > max {
			max = a
		}
	}
	// Target -20 LUFS on 5.1 -> per-channel amplitude ~0.0635.
	if max < 0.05 || max > 0.08 {
		t.Errorf("unexpected channel amplitude %v", max)
	}
}

func TestFixtureRejectsBadOptions(t *testing.T) {
	dir := t.TempDir()
	bad := []FixtureOptions{
		{Layout: "7.1", Seconds: 1, SampleRate: 48000, TargetLoudness: -20},
		{Layout: "stereo", Seconds: 0, SampleRate: 48000, TargetLoudness: -20},
		{Layout: "stereo", Seconds: 1, SampleRate: 1000, TargetLoudness: -20},
		{Layout: "mono", Seconds: 1, SampleRate: 48000, TargetLoudness: 10},
	}
	for i, o := range bad {
		if err := WriteFixture(filepath.Join(dir, "x.wav"), o); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}
